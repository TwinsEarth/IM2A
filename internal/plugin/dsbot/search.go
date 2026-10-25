// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/web-search.js (Apache-2.0)，
// 已修改为 Go 实现：保留 `auto` provider 的候选规划、三种协议响应读取、通用 JSON 源提取、
// Bing/DuckDuckGo 结果页解析与"原生搜索失败再走无密钥兜底"的编排；去掉 DSH 注入
// （ctx.web / ctx.llm / credentials / launchEnvironment）与 provider 目录，改为
// 显式 Route 配置 + 可注入 HTTPDoer，因此可用内嵌 fixture 完全离线测试。
package dsbotplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 上游常量（ds-bot/web-search.js）。
const (
	userAgent         = "deepseek-harness/0.0.1"
	snippetChars      = 500
	outputCap         = 4096
	defaultNativeTO   = 40 * time.Second
	defaultFallbackTO = 15 * time.Second
)

// ---------------------------------------------------------------------------
// 数据模型

// Source 是一条可移植的搜索结果；空的可选字段不臆造（与上游 sourceOf 一致）。
type Source struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

// ProtocolRead 是协议响应读取结果：是否真的跑了搜索、是否被输出上限截断、结构化结果。
type ProtocolRead struct {
	Ran     bool     `json:"ran"`
	Cut     bool     `json:"cut"`
	Sources []Source `json:"sources"`
	Err     string   `json:"err,omitempty"`
}

// PageRead 是结果页解析结果；Error 是"这页不是可解析结果页"的原因。
type PageRead struct {
	Sources []Source `json:"sources"`
	Err     string   `json:"err,omitempty"`
}

// SearchResult 是一次搜索的结果（Backend 说明是哪条路径回答的，与上游一致）。
type SearchResult struct {
	Backend   string   `json:"backend"`
	Sources   []Source `json:"sources"`
	Truncated bool     `json:"truncated"`
}

// Route 是"发起搜索的会话所用的模型路由"的显式配置（替代上游对 DSH 注入的读取）。
type Route struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	API      string            `json:"api"` // anthropic-messages | openai-responses | openai-completions | google-generative-ai | ""
	BaseURL  string            `json:"baseURL"`
	APIKey   string            `json:"-"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// SearchAPI 是宿主在模型协议之外另行提供的搜索 API（上游 SEARCH_APIS 的 Go 形态）。
type SearchAPI struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Hosts         []string       `json:"hosts"`
	Path          string         `json:"path"`
	MaxResults    int            `json:"maxResults,omitempty"`
	MaxQueryChars int            `json:"maxQueryChars,omitempty"`
	Body          map[string]any `json:"body"`
	Auth          []string       `json:"auth,omitempty"`
}

// Candidate 是一条待尝试的原生搜索路径（URL/请求模板/响应读取器）。
type Candidate struct {
	ID        string
	Name      string
	URL       string
	Model     string
	Body      map[string]any
	Headers   map[string]string
	AuthStyle []string
	Read      func([]byte) ProtocolRead
}

// Plan 是原生搜索候选规划结果；无候选时 Reason 说明原因。
type Plan struct {
	Candidates []Candidate
	Reason     string
}

// ---------------------------------------------------------------------------
// HTML / 文本小工具（移植自上游 helpers）

// namedEntities 只保留上游列出的命名实体；未知实体原样保留。
var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'", "nbsp": " ", "ensp": " ", "emsp": " ", "thinsp": " ",
	"middot": "·", "hellip": "…", "mdash": "—", "ndash": "–", "laquo": "«", "raquo": "»", "lsquo": "‘", "rsquo": "’",
	"ldquo": "“", "rdquo": "”", "bull": "•", "copy": "©", "reg": "®", "trade": "™", "zwj": "", "zwnj": "",
}

var entityRe = regexp.MustCompile(`&(#x[0-9a-fA-F]+|#[0-9]+|[a-zA-Z]+);`)

// DecodeEntities 解码 HTML 实体（数字实体与上游列出的命名实体；未知保留原文）。
func DecodeEntities(text string) string {
	return entityRe.ReplaceAllStringFunc(text, func(whole string) string {
		ref := whole[1 : len(whole)-1]
		if ref[0] == '#' {
			base := 10
			digits := ref[1:]
			if ref[1] == 'x' || ref[1] == 'X' {
				base = 16
				digits = ref[2:]
			}
			code, err := strconv.ParseInt(digits, base, 32)
			if err != nil || code <= 0 || code > 0x10FFFF {
				return whole
			}
			return string(rune(code))
		}
		if v, ok := namedEntities[strings.ToLower(ref)]; ok {
			return v
		}
		return whole
	})
}

var (
	scriptStyleRe = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`),
	}
	blockEndTagRe = regexp.MustCompile(`(?is)<(br|/p|/div|/li|/h\d|/td|/tr)\b[^>]*>`)
	anyTagRe      = regexp.MustCompile(`(?is)<[^>]*>`)
	spaceRunRe    = regexp.MustCompile(`\s+`)
)

// TextOf 把 HTML 片段变成纯文本：内联标签消失（保持单词连续），块结束变空格，解码实体。
func TextOf(htmlText string) string {
	for _, re := range scriptStyleRe {
		htmlText = re.ReplaceAllString(htmlText, " ")
	}
	htmlText = blockEndTagRe.ReplaceAllString(htmlText, " ")
	htmlText = anyTagRe.ReplaceAllString(htmlText, "")
	return strings.TrimSpace(spaceRunRe.ReplaceAllString(DecodeEntities(htmlText), " "))
}

// attrOf 从标签属性串里取 attr（双引号/单引号均可），并解码实体；取不到返回空串。
func attrOf(attrs, attr string) string {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(attr) + `\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	m := re.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return DecodeEntities(m[1])
	}
	return DecodeEntities(m[2])
}

// httpURL 只接受 http/https 绝对 URL，返回规范化 href。
func httpURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return u.String(), true
}

// plain 折叠空白并去首尾空白。
func plain(value string) string {
	return strings.TrimSpace(spaceRunRe.ReplaceAllString(value, " "))
}

// clip 按码点截断并补省略号（上游 clip 的 Go 等价）。
func clip(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// sourceOf 用纯文本字段构造一条 Source；URL 非法或全部为空时返回 false（不臆造）。
func sourceOf(rawURL, title, snippet, publishedAt string) (Source, bool) {
	href, ok := httpURL(rawURL)
	if !ok {
		return Source{}, false
	}
	src := Source{URL: href}
	if t := plain(title); t != "" {
		src.Title = clip(t, 300)
	}
	if s := plain(snippet); s != "" {
		src.Snippet = clip(s, snippetChars)
	}
	if d := strings.TrimSpace(publishedAt); d != "" {
		src.PublishedAt = d
	}
	return src, true
}

// dedupe 按 URL 去重：保留首次出现顺序；后来的同 URL 只补首次缺失的字段。
func dedupe(sources []Source) []Source {
	index := make(map[string]int, len(sources))
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if i, ok := index[s.URL]; ok {
			if out[i].Title == "" {
				out[i].Title = s.Title
			}
			if out[i].Snippet == "" {
				out[i].Snippet = s.Snippet
			}
			if out[i].PublishedAt == "" {
				out[i].PublishedAt = s.PublishedAt
			}
			continue
		}
		index[s.URL] = len(out)
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// 原生搜索：三种协议的响应读取（只取结构化来源，绝不把模型写的散文当结果）

type messagesBlock struct {
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Content   json.RawMessage   `json:"content"`
	Citations []messagesCite    `json:"citations"`
}

type messagesCite struct {
	URL       string `json:"url"`
	CitedText string `json:"cited_text"`
}

type messagesResult struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
	PageAge string `json:"page_age"`
}

type messagesError struct {
	Type      string `json:"type"`
	ErrorCode string `json:"error_code"`
}

// ReadMessages 读取 Anthropic Messages + `web_search_20250305` server tool 的响应。
func ReadMessages(body []byte) (ProtocolRead, error) {
	var doc struct {
		Content    []messagesBlock `json:"content"`
		StopReason string          `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ProtocolRead{}, err
	}
	excerpts := make(map[string]string)
	results := make([]messagesResult, 0)
	sawToolUse := false
	failedCode := ""
	for _, block := range doc.Content {
		switch block.Type {
		case "server_tool_use":
			sawToolUse = true
		case "web_search_tool_result":
			var items []messagesResult
			if err := json.Unmarshal(block.Content, &items); err == nil {
				results = append(results, items...)
				continue
			}
			var failure messagesError
			if err := json.Unmarshal(block.Content, &failure); err == nil && failure.Type == "web_search_tool_result_error" {
				failedCode = failure.ErrorCode
			}
		case "text":
			for _, cite := range block.Citations {
				if _, ok := httpURL(cite.URL); ok && strings.TrimSpace(cite.CitedText) != "" {
					if _, seen := excerpts[cite.URL]; !seen {
						excerpts[cite.URL] = cite.CitedText
					}
				}
			}
		}
	}
	sources := make([]Source, 0, len(results))
	for _, item := range results {
		if item.Type != "web_search_result" {
			continue
		}
		snippet := excerpts[item.URL]
		if snippet == "" {
			snippet = item.Content
		}
		if src, ok := sourceOf(item.URL, item.Title, snippet, item.PageAge); ok {
			sources = append(sources, src)
		}
	}
	read := ProtocolRead{Ran: len(results) > 0 || sawToolUse, Cut: doc.StopReason == "max_tokens", Sources: dedupe(sources)}
	if len(read.Sources) == 0 && failedCode != "" {
		read.Err = "web search failed: " + failedCode
	}
	return read, nil
}

type responsesItem struct {
	Type    string `json:"type"`
	Action  struct {
		Sources []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"sources"`
	} `json:"action"`
	Content []struct {
		Type        string `json:"type"`
		Annotations []struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"annotations"`
	} `json:"content"`
}

// ReadResponses 读取 OpenAI Responses + `web_search` tool 的响应。
func ReadResponses(body []byte) (ProtocolRead, error) {
	var doc struct {
		Output    []responsesItem `json:"output"`
		Citations []json.RawMessage `json:"citations"`
		Status    string            `json:"status"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ProtocolRead{}, err
	}
	found := make([]Source, 0)
	calls := 0
	for _, item := range doc.Output {
		if item.Type == "web_search_call" {
			calls++
			for _, src := range item.Action.Sources {
				if s, ok := sourceOf(src.URL, src.Title, "", ""); ok {
					found = append(found, s)
				}
			}
		}
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			for _, note := range part.Annotations {
				if note.Type != "url_citation" {
					continue
				}
				if s, ok := sourceOf(note.URL, note.Title, "", ""); ok {
					found = append(found, s)
				}
			}
		}
	}
	for _, raw := range doc.Citations {
		var asString string
		if json.Unmarshal(raw, &asString) == nil {
			if s, ok := sourceOf(asString, "", "", ""); ok {
				found = append(found, s)
			}
			continue
		}
		var asObject struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &asObject) == nil {
			if s, ok := sourceOf(asObject.URL, asObject.Title, "", ""); ok {
				found = append(found, s)
			}
		}
	}
	sources := dedupe(found)
	return ProtocolRead{Ran: calls > 0 || len(sources) > 0, Cut: doc.Status == "incomplete", Sources: sources}, nil
}

type geminiCandidate struct {
	GroundingMetadata *struct {
		GroundingChunks []struct {
			Web struct {
				URI   string `json:"uri"`
				Title string `json:"title"`
			} `json:"web"`
		} `json:"groundingChunks"`
	} `json:"groundingMetadata"`
	FinishReason string `json:"finishReason"`
}

// ReadGemini 读取 Gemini `google_search` tool 的响应。
func ReadGemini(body []byte) (ProtocolRead, error) {
	var doc struct {
		Candidates []geminiCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ProtocolRead{}, err
	}
	found := make([]Source, 0)
	ran := false
	cut := false
	for _, candidate := range doc.Candidates {
		if candidate.GroundingMetadata != nil {
			ran = true
			for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
				if s, ok := sourceOf(chunk.Web.URI, chunk.Web.Title, "", ""); ok {
					found = append(found, s)
				}
			}
		}
		if candidate.FinishReason == "MAX_TOKENS" {
			cut = true
		}
	}
	return ProtocolRead{Ran: ran, Cut: cut, Sources: dedupe(found)}, nil
}

// ---------------------------------------------------------------------------
// 通用 JSON 源提取（声明的搜索 API：不知道 shape，只按"带 http URL 的对象"识别）

var (
	urlKeys     = []string{"url", "uri", "link"}
	titleKeys   = []string{"title", "name", "site_name"}
	snippetKeys = []string{"snippet", "summary", "content", "description", "cited_text", "text"}
	dateKeys    = []string{"published_date", "publish_date", "publish_time", "page_age", "date", "last_updated"}
	// 媒体链接不是结果；回显请求 tools 的响应也不是搜索。
	notSources = map[string]bool{
		"image_url": true, "images": true, "image": true, "icon": true, "favicon": true,
		"logo": true, "logo_url": true, "thumbnail": true, "tools": true,
	}
	searchMarkRe = regexp.MustCompile(`(?i)search|grounding|citation`)
)

func firstText(record map[string]any, keys []string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func looksLikeURL(value any) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	return httpURL(s)
}

// FindSources 遍历任意 JSON，把每个带 http(s) URL 的对象读成一条 Source。
func FindSources(body []byte) ([]Source, error) {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	found := make([]Source, 0)
	var visit func(value any, depth int)
	visit = func(value any, depth int) {
		if depth > 16 || value == nil {
			return
		}
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				visit(item, depth+1)
			}
		case map[string]any:
			for _, key := range urlKeys {
				if href, ok := looksLikeURL(typed[key]); ok {
					if src, ok := sourceOf(href, firstText(typed, titleKeys), firstText(typed, snippetKeys), firstText(typed, dateKeys)); ok {
						found = append(found, src)
					}
					break
				}
			}
			for key, child := range typed {
				if notSources[key] {
					continue
				}
				visit(child, depth+1)
			}
		}
	}
	visit(decoded, 0)
	return dedupe(found), nil
}

// ShowsSearch 只按 key/type 标记判断"这个 JSON 响应看起来跑过搜索"，不看散文。
func ShowsSearch(body []byte) bool {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return false
	}
	var visit func(value any, depth int) bool
	visit = func(value any, depth int) bool {
		if depth > 16 || value == nil {
			return false
		}
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				if visit(item, depth+1) {
					return true
				}
			}
		case map[string]any:
			for key, child := range typed {
				if notSources[key] {
					continue
				}
				if searchMarkRe.MatchString(key) || key == "results" {
					return true
				}
				if key == "type" {
					if s, ok := child.(string); ok && searchMarkRe.MatchString(s) {
						return true
					}
				}
				if visit(child, depth+1) {
					return true
				}
			}
		}
		return false
	}
	return visit(decoded, 0)
}

// ReadDeclared 读取一个"声明的搜索 API"的响应：任意结构化来源都算数。
func ReadDeclared(body []byte) (ProtocolRead, error) {
	sources, err := FindSources(body)
	if err != nil {
		return ProtocolRead{}, err
	}
	return ProtocolRead{Ran: len(sources) > 0 || ShowsSearch(body), Sources: sources}, nil
}

// ---------------------------------------------------------------------------
// 无密钥兜底：公开结果页解析

// DecodeBingURL 还原 Bing 点击跳转链接（bing.com/ck/a?...&u=a1<base64url>）；失败返回空串。
func DecodeBingURL(href string) string {
	normalized, ok := httpURL(href)
	if !ok {
		return ""
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "bing.com" && !strings.HasSuffix(host, ".bing.com") {
		return normalized
	}
	if parsed.Path != "/ck/a" {
		return ""
	}
	target := parsed.Query().Get("u")
	if !strings.HasPrefix(target, "a1") {
		return ""
	}
	encoded := strings.TrimRight(target[2:], "=")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	decoded, ok := httpURL(string(raw))
	if !ok {
		return ""
	}
	return decoded
}

var (
	bingAlgoRe       = regexp.MustCompile(`(?is)<li\b[^>]*class\s*=\s*"[^"]*\bb_algo\b[^"]*"[^>]*>`)
	bingHeadingRe    = regexp.MustCompile(`(?is)<h2\b[^>]*>\s*<a\b([^>]*)>(.*?)</a>`)
	bingCaptionRe    = regexp.MustCompile(`(?is)<div\b[^>]*\bclass\s*=\s*"[^"]*\bb_caption\b`)
	bingParagraphRe  = regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	bingNoResultsRe  = regexp.MustCompile(`(?i)\bclass\s*=\s*"[^"]*\bb_no\b`)
	bingResultsIDRe  = regexp.MustCompile(`(?i)id\s*=\s*"b_results"`)
)

// ParseBing 解析 Bing 结果页 → 来源；不是结果页时返回 Error 原因（与上游判据一致）。
func ParseBing(htmlText string) PageRead {
	starts := bingAlgoRe.FindAllStringIndex(htmlText, -1)
	sources := make([]Source, 0, len(starts))
	for i, start := range starts {
		end := len(htmlText)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		if end > start[1]+12000 {
			end = start[1] + 12000
		}
		if end > len(htmlText) {
			end = len(htmlText)
		}
		block := htmlText[start[1]:end]
		heading := bingHeadingRe.FindStringSubmatch(block)
		if heading == nil {
			continue
		}
		href := attrOf(heading[1], "href")
		snippet := ""
		caption := block
		if loc := bingCaptionRe.FindStringIndex(block); loc != nil {
			caption = block[loc[0]:]
		}
		if paragraph := bingParagraphRe.FindStringSubmatch(caption); paragraph != nil {
			snippet = TextOf(paragraph[1])
		}
		if src, ok := sourceOf(DecodeBingURL(href), TextOf(heading[2]), snippet, ""); ok {
			sources = append(sources, src)
		}
	}
	parsed := dedupe(sources)
	if len(parsed) > 0 {
		return PageRead{Sources: parsed}
	}
	if bingNoResultsRe.MatchString(htmlText) {
		return PageRead{Sources: []Source{}}
	}
	if bingResultsIDRe.MatchString(htmlText) {
		return PageRead{Err: "results page had no parsable results"}
	}
	return PageRead{Err: "not a results page (bot check or changed layout)"}
}

var (
	ddgBotCheckRe  = regexp.MustCompile(`(?i)anomaly-modal|bots use DuckDuckGo too|challenge-form`)
	ddgLinkRe      = regexp.MustCompile(`(?is)<a\b([^>]*\bclass\s*=\s*"[^"]*\bresult__a\b[^"]*"[^>]*)>(.*?)</a>`)
	ddgSnippetRe   = regexp.MustCompile(`(?is)<(?:a|div|td)\b[^>]*\bclass\s*=\s*"[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</(?:a|div|td)>`)
	ddgNoResultsRe = regexp.MustCompile(`(?i)\bresult--no-results\b|\bno-results\b`)
)

// ParseDuckDuckGo 解析 DuckDuckGo HTML 结果页 → 来源；跳过广告、识别机器人检查页。
func ParseDuckDuckGo(htmlText string) PageRead {
	if ddgBotCheckRe.MatchString(htmlText) {
		return PageRead{Err: "bot check page"}
	}
	matches := ddgLinkRe.FindAllStringSubmatchIndex(htmlText, -1)
	sources := make([]Source, 0, len(matches))
	for i, match := range matches {
		attrs := htmlText[match[2]:match[3]]
		inner := htmlText[match[4]:match[5]]
		href := attrOf(attrs, "href")
		target := ""
		if parsed, err := url.Parse(href); err == nil {
			// base.ResolveReference(ref)：协议相对链接（//host/path）按 https 解析。
			base := &url.URL{Scheme: "https", Host: "duckduckgo.com"}
			resolved := base.ResolveReference(parsed)
			host := strings.ToLower(resolved.Hostname())
			if host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com") {
				// /l/ 是真实跳转；/y.js 是广告。
				if resolved.Path == "/l/" {
					target = resolved.Query().Get("uddg")
				}
			} else {
				target = resolved.String()
			}
		}
		restStart := match[1]
		restEnd := len(htmlText)
		if i+1 < len(matches) {
			restEnd = matches[i+1][0]
		}
		if restEnd > restStart+6000 {
			restEnd = restStart + 6000
		}
		if restEnd > len(htmlText) {
			restEnd = len(htmlText)
		}
		rest := htmlText[restStart:restEnd]
		snippet := ""
		if found := ddgSnippetRe.FindStringSubmatch(rest); found != nil {
			snippet = TextOf(found[1])
		}
		if src, ok := sourceOf(target, TextOf(inner), snippet, ""); ok {
			sources = append(sources, src)
		}
	}
	parsed := dedupe(sources)
	if len(parsed) > 0 {
		return PageRead{Sources: parsed}
	}
	if ddgNoResultsRe.MatchString(htmlText) {
		return PageRead{Sources: []Source{}}
	}
	return PageRead{Err: "not a results page (bot check or changed layout)"}
}

// Engine 是一个无密钥结果页引擎。
type Engine struct {
	Name string
	URL  func(query string) string
	Parse func(htmlText string) PageRead
}

// Engines 移植上游 ENGINES（顺序即尝试顺序）。
var Engines = map[string]Engine{
	"bing": {
		Name:  "Bing",
		URL:   func(query string) string { return "https://www.bing.com/search?q=" + url.QueryEscape(query) },
		Parse: ParseBing,
	},
	"duckduckgo": {
		Name:  "DuckDuckGo",
		URL:   func(query string) string { return "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query) },
		Parse: ParseDuckDuckGo,
	},
}

// DefaultEngines 是上游默认引擎顺序。
var DefaultEngines = []string{"bing", "duckduckgo"}

// BuiltinSearchAPIs 逐条移植上游 SEARCH_APIS（宿主在模型协议之外声明的搜索 API）。
var BuiltinSearchAPIs = []SearchAPI{
	{
		ID: "ollama", Name: "Ollama web search API", Hosts: []string{"ollama.com"},
		Path: "/api/web_search", MaxResults: 10,
		Body: map[string]any{"query": "$query", "max_results": "$maxResults"},
	},
	{
		ID: "kimi", Name: "Kimi search API", Hosts: []string{"api.moonshot.ai", "api.moonshot.cn"},
		Path: "/v1/tools/search", MaxResults: 20,
		Body: map[string]any{"text_query": "$query", "limit": "$maxResults", "timeout_seconds": 30},
	},
	{
		ID: "zhipu", Name: "Zhipu web search API", Hosts: []string{"open.bigmodel.cn", "api.z.ai"},
		Path: "/api/paas/v4/web_search", MaxResults: 50, MaxQueryChars: 70,
		Body: map[string]any{"search_query": "$query", "search_engine": "search_std", "count": "$maxResults"},
	},
	{
		ID: "mimo", Name: "MiMo web_search plugin", Hosts: []string{"xiaomimimo.com", "*.xiaomimimo.com"},
		Path: "/v1/chat/completions", MaxResults: 5, Auth: []string{"bearer", "api-key"},
		Body: map[string]any{
			"model":                 "$model",
			"messages":              []any{map[string]any{"role": "user", "content": "$query"}},
			"tools":                 []any{map[string]any{"type": "web_search", "force_search": true, "max_keyword": 1, "limit": "$maxResults"}},
			"max_completion_tokens": 256,
			"thinking":              map[string]any{"type": "disabled"},
			"stream":                false,
		},
	},
}

// ---------------------------------------------------------------------------
// Provider 候选规划（移植 planNative）

func hostMatches(patterns []string, host string) bool {
	host = strings.ToLower(host)
	for _, pattern := range patterns {
		pattern = strings.ToLower(pattern)
		if strings.HasPrefix(pattern, "*.") {
			if strings.HasSuffix(host, pattern[1:]) {
				return true
			}
			continue
		}
		if host == pattern {
			return true
		}
	}
	return false
}

// fillTemplate 递归替换模板里的 $query / $model / $maxResults 占位。
func fillTemplate(template any, values map[string]string) any {
	switch typed := template.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = fillTemplate(item, values)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = fillTemplate(value, values)
		}
		return out
	case string:
		if replacement, ok := values[typed]; ok {
			return replacement
		}
		return typed
	default:
		return template
	}
}

// templateUsesModel 判断模板是否引用 $model。
func templateUsesModel(template any) bool {
	switch typed := template.(type) {
	case []any:
		for _, item := range typed {
			if templateUsesModel(item) {
				return true
			}
		}
	case map[string]any:
		for _, value := range typed {
			if templateUsesModel(value) {
				return true
			}
		}
	case string:
		return typed == "$model"
	}
	return false
}

// originOf 返回 URL 的 scheme://host（上游 new URL(url).origin）。
func originOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func declaredCandidate(api SearchAPI, origin, model string) (Candidate, bool) {
	fullURL, err := url.Parse(strings.TrimRight(origin, "/") + api.Path)
	if err != nil {
		return Candidate{}, false
	}
	fullOrigin, err := originOf(fullURL.String())
	if err != nil || fullOrigin != origin {
		// `//other.example` 这类路径不得把密钥带到别的源。
		return Candidate{}, false
	}
	auth := api.Auth
	if len(auth) == 0 {
		auth = []string{"bearer"}
	}
	candidate := Candidate{
		ID:        api.ID,
		Name:      api.Name,
		URL:       fullURL.String(),
		Headers:   map[string]string{},
		AuthStyle: auth,
		Read:      readDeclaredFunc,
	}
	if api.Name == "" {
		candidate.Name = api.ID + " search API"
	}
	if templateUsesModel(api.Body) {
		candidate.Model = model
	}
	candidate.Body = map[string]any{"__template": api.Body, "__maxQueryChars": api.MaxQueryChars}
	return candidate, true
}

// readDeclaredFunc 适配 ReadDeclared 为 []byte → ProtocolRead 且吞掉 JSON 解析错误。
func readDeclaredFunc(body []byte) ProtocolRead {
	read, err := ReadDeclared(body)
	if err != nil {
		return ProtocolRead{Ran: false, Sources: nil}
	}
	return read
}

// messagesRoot 让 URL 根带且仅带一层 /v1（与上游一致）。
func messagesRoot(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return strings.TrimRight(base, "/")
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		return strings.TrimRight(base, "/")
	}
	return strings.TrimRight(base, "/") + "/v1"
}

type protocolShape struct {
	name    string
	url     func(base, model string) string
	headers map[string]string
	body    func(query, model string, maxResults int) map[string]any
	read    func([]byte) ProtocolRead
	auth    []string
}

func messagesRead(body []byte) ProtocolRead {
	read, err := ReadMessages(body)
	if err != nil {
		return ProtocolRead{}
	}
	return read
}

func responsesRead(body []byte) ProtocolRead {
	read, err := ReadResponses(body)
	if err != nil {
		return ProtocolRead{}
	}
	return read
}

func geminiRead(body []byte) ProtocolRead {
	read, err := ReadGemini(body)
	if err != nil {
		return ProtocolRead{}
	}
	return read
}

// protocolSearches 是每种 wire protocol 的标准搜索工具（上游 PROTOCOL_SEARCHES 的 Go 形态）。
var protocolSearches = map[string]protocolShape{
	"messages": {
		name:    "Anthropic Messages web_search tool",
		url:     func(base, _ string) string { return messagesRoot(base) + "/messages" },
		headers: map[string]string{"anthropic-version": "2023-06-01"},
		body: func(query, model string, _ int) map[string]any {
			return map[string]any{
				"model":      model,
				"max_tokens": outputCap,
				"messages": []any{map[string]any{
					"role":    "user",
					"content": []any{map[string]any{"type": "text", "text": "Perform a web search for the query: " + query}},
				}},
				"tools": []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 1}},
			}
		},
		read: messagesRead,
		// Anthropic 读 x-api-key；兼容网关常读 Bearer，故两者都带。
		auth: []string{"x-api-key", "bearer"},
	},
	"responses": {
		name: "OpenAI Responses web_search tool",
		url:  func(base, _ string) string { return strings.TrimRight(base, "/") + "/responses" },
		body: func(query, model string, _ int) map[string]any {
			return map[string]any{
				"model":             model,
				"input":             "Perform a web search for the query: " + query,
				"tools":             []any{map[string]any{"type": "web_search"}},
				"max_output_tokens": outputCap,
			}
		},
		read: responsesRead,
		auth: []string{"bearer"},
	},
	"gemini": {
		name: "Gemini google_search tool",
		url: func(base, model string) string {
			return strings.TrimRight(base, "/") + "/models/" + url.PathEscape(model) + ":generateContent"
		},
		body: func(query, _ string, _ int) map[string]any {
			return map[string]any{
				"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Perform a web search for the query: " + query}}}},
				"tools":            []any{map[string]any{"google_search": map[string]any{}}},
				"generationConfig": map[string]any{"maxOutputTokens": outputCap},
			}
		},
		read: geminiRead,
		auth: []string{"x-goog-api-key"},
	},
}

var protocolByAPI = map[string][]string{
	"anthropic-messages":   {"messages"},
	"openai-responses":     {"responses"},
	"openai-completions":   {"responses", "messages"},
	"google-generative-ai": {"gemini"},
}

var unknownProtocolShapes = []string{"responses", "messages"}

// PlanNative 规划一次原生搜索要尝试的候选（最便宜的先用：声明的搜索 API 在协议工具之前）。
func PlanNative(route Route, searchAPIs []SearchAPI) Plan {
	base, ok := httpURL(route.BaseURL)
	if !ok {
		return Plan{Reason: fmt.Sprintf("route %q has no known endpoint", route.Provider)}
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return Plan{Reason: fmt.Sprintf("route %q has no known endpoint", route.Provider)}
	}
	origin := parsed.Scheme + "://" + parsed.Host
	model := strings.TrimSpace(route.Model)
	apis := searchAPIs
	if apis == nil {
		apis = BuiltinSearchAPIs
	}
	candidates := make([]Candidate, 0, 4)
	for _, api := range apis {
		if !hostMatches(api.Hosts, parsed.Hostname()) {
			continue
		}
		if model == "" && templateUsesModel(api.Body) {
			continue
		}
		if candidate, ok := declaredCandidate(api, origin, model); ok {
			candidates = append(candidates, candidate)
		}
	}
	shapes := protocolByAPI[route.API]
	if route.API == "" {
		shapes = unknownProtocolShapes
	}
	if model != "" {
		for _, id := range shapes {
			shape := protocolSearches[id]
			candidates = append(candidates, Candidate{
				ID:        id,
				Name:      shape.name,
				URL:       shape.url(base, model),
				Model:     model,
				Body:      map[string]any{"__protocol": id},
				Headers:   shape.headers,
				AuthStyle: shape.auth,
				Read:      shape.read,
			})
		}
	}
	if len(candidates) > 0 {
		return Plan{Candidates: candidates}
	}
	if model == "" && len(shapes) > 0 {
		return Plan{Reason: fmt.Sprintf("route %q has no current model to search with", route.Provider)}
	}
	return Plan{Reason: fmt.Sprintf("route %q speaks %s, which has no standard search tool, and %s declares no search API",
		route.Provider, labelOrEmpty(route.API, "an unknown protocol"), parsed.Host)}
}

func labelOrEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// ---------------------------------------------------------------------------
// Searcher：把"原生优先、无密钥兜底"的编排做成可注入依赖的 Go 形态

// HTTPDoer 是 Searcher/桥接使用的最小 HTTP 抽象；*http.Client 天然满足。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// SearcherOptions 是 Searcher 的显式配置（替代上游的 DSH 注入与 plugin config）。
type SearcherOptions struct {
	Route           Route
	SearchAPIs      []SearchAPI
	Doer            HTTPDoer
	DisableNative   bool
	DisableFallback bool
	Engines         []string
	NativeTimeout   time.Duration
	FallbackTimeout time.Duration
	MaxResults      int
}

// Searcher 是 `auto` 搜索 provider 的 Go 实现：原生搜索优先，失败后走无密钥结果页兜底。
type Searcher struct {
	opts SearcherOptions
}

// NewSearcher 构造 Searcher；零值选项按上游默认值补齐。
func NewSearcher(opts SearcherOptions) *Searcher {
	if opts.Engines == nil {
		opts.Engines = DefaultEngines
	}
	if opts.NativeTimeout <= 0 {
		opts.NativeTimeout = defaultNativeTO
	}
	if opts.FallbackTimeout <= 0 {
		opts.FallbackTimeout = defaultFallbackTO
	}
	return &Searcher{opts: opts}
}

// SearchUnavailableError 两条路径都失败；错误信息里明确要求模型不要编造 URL（上游同款约束）。
type SearchUnavailableError struct {
	NativeReason   string
	FallbackReason string
}

func (e *SearchUnavailableError) Error() string {
	return "Web search is unavailable for this query.\nNative search: " + e.NativeReason +
		".\nKeyless fallback: " + e.FallbackReason +
		".\nDo not guess URLs or present pages you remember as search results. If you have a browser tool, " +
		"open a search engine in it; otherwise tell the user that web search is unavailable and why."
}

// Search 执行一次 `auto` 搜索。
func (s *Searcher) Search(ctx context.Context, query string) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, errors.New("dsbotplugin: web search: empty query")
	}
	if s.opts.Doer == nil {
		return SearchResult{}, errors.New("dsbotplugin: web search: no HTTPDoer configured")
	}
	nativeReason := "turned off (native: false)"
	if !s.opts.DisableNative {
		result, reason := s.searchNative(ctx, query)
		if result != nil {
			return *result, nil
		}
		nativeReason = reason
	}
	fallbackReason := "turned off (fallback: false)"
	if !s.opts.DisableFallback {
		result, reason := s.searchFallback(ctx, query)
		if result != nil {
			return *result, nil
		}
		fallbackReason = reason
	}
	return SearchResult{}, &SearchUnavailableError{NativeReason: nativeReason, FallbackReason: fallbackReason}
}

// searchNative 尝试原生搜索候选；成功返回结果，失败返回原因链（nil 结果）。
func (s *Searcher) searchNative(ctx context.Context, query string) (*SearchResult, string) {
	plan := PlanNative(s.opts.Route, s.opts.SearchAPIs)
	if len(plan.Candidates) == 0 {
		return nil, plan.Reason
	}
	deadline := time.Now().Add(s.opts.NativeTimeout)
	notes := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			notes = append(notes, candidate.Name+": skipped, out of time")
			continue
		}
		note, result := s.attemptNative(ctx, candidate, query, remaining)
		if result != nil {
			return result, ""
		}
		notes = append(notes, candidate.Name+": "+note)
	}
	return nil, fmt.Sprintf("route %q: %s", s.opts.Route.Provider, strings.Join(notes, "; "))
}

func (s *Searcher) attemptNative(ctx context.Context, candidate Candidate, query string, budget time.Duration) (string, *SearchResult) {
	body, headers, err := s.candidateRequest(candidate, query)
	if err != nil {
		return "could not build request: " + err.Error(), nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "could not encode request: " + err.Error(), nil
	}
	attemptCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, candidate.URL, bytes.NewReader(payload))
	if err != nil {
		return "bad request: " + err.Error(), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for key, value := range s.opts.Route.Headers {
		req.Header.Set(key, value)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	s.applyAuth(req, candidate)
	resp, err := s.opts.Doer.Do(req)
	if err != nil {
		return "request failed: " + err.Error(), nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "HTTP " + strconv.Itoa(resp.StatusCode) + errorDetail(raw), nil
	}
	if readErr != nil {
		return "could not read response: " + readErr.Error(), nil
	}
	read := candidate.Read(raw)
	if len(read.Sources) > 0 {
		return "", &SearchResult{Backend: "native: " + candidate.Name, Sources: read.Sources}
	}
	if read.Err != "" {
		return read.Err, nil
	}
	if read.Ran {
		return "returned no results", nil
	}
	if read.Cut {
		return "stopped at the output cap before searching", nil
	}
	return "answered without running a search, so it does not seem to support this one", nil
}

// candidateRequest 按候选类型构造请求体与额外头。
func (s *Searcher) candidateRequest(candidate Candidate, query string) (map[string]any, map[string]string, error) {
	maxResults := s.opts.MaxResults
	if maxResults <= 0 {
		maxResults = 8
	}
	if protocol, ok := candidate.Body["__protocol"].(string); ok {
		shape, ok := protocolSearches[protocol]
		if !ok {
			return nil, nil, fmt.Errorf("unknown protocol %q", protocol)
		}
		return shape.body(query, candidate.Model, maxResults), candidate.Headers, nil
	}
	template, _ := candidate.Body["__template"]
	maxQueryChars, _ := candidate.Body["__maxQueryChars"].(int)
	effectiveQuery := query
	if maxQueryChars > 0 {
		if runes := []rune(query); len(runes) > maxQueryChars {
			effectiveQuery = string(runes[:maxQueryChars])
		}
	}
	filled, ok := fillTemplate(template, map[string]string{
		"$query":      effectiveQuery,
		"$model":      candidate.Model,
		"$maxResults": strconv.Itoa(maxResults),
	}).(map[string]any)
	if !ok {
		return nil, nil, errors.New("declared search API body must be an object")
	}
	return filled, candidate.Headers, nil
}

// applyAuth 按候选的认证风格附加凭据头（密钥只去路由自己的源，与上游一致）。
func (s *Searcher) applyAuth(req *http.Request, candidate Candidate) {
	key := strings.TrimSpace(s.opts.Route.APIKey)
	if key == "" {
		return
	}
	for _, style := range candidate.AuthStyle {
		switch style {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+key)
		case "x-api-key":
			req.Header.Set("x-api-key", key)
		case "api-key":
			req.Header.Set("api-key", key)
		case "x-goog-api-key":
			req.Header.Set("x-goog-api-key", key)
		}
	}
}

// errorDetail 从错误响应正文抽一句可读信息（上游 errorDetail 的 Go 等价）。
func errorDetail(raw []byte) string {
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	for _, key := range []string{"error", "message", "msg"} {
		switch value := parsed[key].(type) {
		case string:
			if detail := strings.TrimSpace(value); detail != "" {
				return ": " + clip(detail, 200)
			}
		case map[string]any:
			if msg, ok := value["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return ": " + clip(strings.TrimSpace(msg), 200)
			}
		}
	}
	return ""
}

// searchFallback 依次抓公开结果页并解析；成功（含空结果页）返回结果。
func (s *Searcher) searchFallback(ctx context.Context, query string) (*SearchResult, string) {
	deadline := time.Now().Add(s.opts.FallbackTimeout)
	attempts := make([]string, 0, len(s.opts.Engines))
	var empty *SearchResult
	for _, id := range s.opts.Engines {
		engine, ok := Engines[id]
		if !ok {
			attempts = append(attempts, id+": unknown engine")
			continue
		}
		engineURL := engine.URL(query)
		remaining := time.Until(deadline)
		if remaining <= 0 {
			attempts = append(attempts, engine.Name+": skipped, out of time")
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, remaining)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, engineURL, nil)
		if err != nil {
			cancel()
			attempts = append(attempts, engine.Name+": bad request")
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := s.opts.Doer.Do(req)
		if err != nil {
			cancel()
			attempts = append(attempts, engine.Name+": "+err.Error())
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			attempts = append(attempts, engine.Name+": HTTP "+strconv.Itoa(resp.StatusCode))
			continue
		}
		if readErr != nil {
			attempts = append(attempts, engine.Name+": could not read page")
			continue
		}
		parsed := engine.Parse(string(raw))
		if parsed.Err != "" {
			attempts = append(attempts, engine.Name+": "+parsed.Err)
			continue
		}
		result := &SearchResult{
			Backend:   "fallback: " + engine.Name,
			Sources:   parsed.Sources,
			Truncated: false,
		}
		if len(parsed.Sources) == 0 {
			if empty == nil {
				empty = result
			}
			attempts = append(attempts, engine.Name+": no results")
			continue
		}
		return result, ""
	}
	if empty != nil {
		return empty, ""
	}
	return nil, strings.Join(attempts, "; ")
}
