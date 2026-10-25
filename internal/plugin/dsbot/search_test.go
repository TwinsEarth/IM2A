// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/test/web-search.test.js 的 fixture 与断言意图
// (Apache-2.0)，已修改为 Go 测试：全部解析用内嵌 fixture，搜索路径用注入的 fake
// HTTPDoer，测试**不访问任何真实外网**（仅插件门面用例使用 loopback httptest）。

package dsbotplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 内嵌 fixture（结构取自上游 test/fixtures 与 test/web-search.test.js）

const fixtureMessagesHit = `{
  "content": [
    {"type": "server_tool_use", "name": "web_search"},
    {"type": "web_search_tool_result", "content": [
      {"type": "web_search_result", "url": "https://a.example/", "title": "A", "page_age": "2 days ago"},
      {"type": "web_search_result", "url": "https://a.example/", "title": "A again"},
      {"type": "web_search_result", "url": "ftp://bad.example/", "title": "bad"}
    ]},
    {"type": "text", "text": "The model says https://made-up.example/ is relevant.",
     "citations": [{"url": "https://a.example/", "cited_text": "quoted excerpt"}]}
  ],
  "stop_reason": "end_turn"
}`

const fixtureMessagesNoSearch = `{"content": [{"type": "text", "text": "I think the answer is 42."}]}`

const fixtureMessagesError = `{"content": [{"type": "web_search_tool_result", "content": {"type": "web_search_tool_result_error", "error_code": "max_uses_exceeded"}}]}`

const fixtureResponsesHit = `{
  "output": [
    {"type": "web_search_call", "action": {"sources": [{"type": "url", "url": "https://o.example/"}]}},
    {"type": "message", "content": [{"type": "output_text", "text": "answer",
      "annotations": [
        {"type": "url_citation", "url": "https://o.example/", "title": "O"},
        {"type": "url_citation", "url": "https://p.example/", "title": "P"}
      ]}]}
  ],
  "citations": ["https://x.example/"],
  "status": "completed"
}`

const fixtureResponsesIncomplete = `{"status": "incomplete", "output": []}`

const fixtureGeminiHit = `{
  "candidates": [
    {"groundingMetadata": {"groundingChunks": [
      {"web": {"uri": "https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc", "title": "g.example"}}
    ]}},
    {"content": {}, "finishReason": "STOP"}
  ]
}`

const fixtureGeminiMaxTokens = `{"candidates": [{"finishReason": "MAX_TOKENS"}]}`

const fixtureDeclared = `{
  "results": [{"title": "O", "url": "https://ollama.example/", "content": "page text"}],
  "images": [{"url": "https://img.example/a.png"}],
  "tools": [{"type": "web_search", "url": "https://echo.example/"}],
  "items": [{"link": "https://l.example/", "site_name": "L", "thumbnail": {"url": "https://img.example/t.png"}}]
}`

// bingFixture 复刻上游 bing.html 的结构（b_algo 列表 + ck/a 跳转 + b_caption 摘要）。
func bingFixture(encodedTargets ...string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en"><body><ol id="b_results">`)
	for i, encoded := range encodedTargets {
		b.WriteString(`<li class="b_algo"><h2><a href="https://www.bing.com/ck/a?!&amp;&amp;p=fixture&amp;u=a1`)
		b.WriteString(encoded)
		b.WriteString(`&amp;ntb=1"><strong>GitHub</strong> - deep/seek</a></h2>`)
		b.WriteString(`<div class="b_caption"><p class="b_lineclamp2">Snippet <b>`)
		b.WriteString([]string{"one", "two", "three"}[i%3])
		b.WriteString(`</b> &amp; more &#8230;</p></div></li>`)
	}
	b.WriteString(`</ol></body></html>`)
	return b.String()
}

// ddgFixture 复刻上游 duckduckgo.html 的结构（广告 /y.js 在前，结果用 /l/?uddg= 跳转）。
func ddgFixture(encodedTargets ...string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body><div id="links">`)
	b.WriteString(`<div class="result result--ad"><h2 class="result__title">`)
	b.WriteString(`<a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad_domain=example.com&amp;ad_provider=bingv7aa">Sponsored &middot; Example</a>`)
	b.WriteString(`</h2><a class="result__snippet" href="https://duckduckgo.com/y.js">Buy now.</a></div>`)
	for i, encoded := range encodedTargets {
		b.WriteString(`<div class="result web-result"><h2 class="result__title">`)
		b.WriteString(`<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=`)
		b.WriteString(encoded)
		b.WriteString(`&amp;rut=abc123"><b>Result</b> `)
		b.WriteString([]string{"One", "Two", "Three"}[i%3])
		b.WriteString(`</a></h2><a class="result__snippet" href="//duckduckgo.com/l/?uddg=`)
		b.WriteString(encoded)
		b.WriteString(`">Snippet &amp; more &#8230;</a></div>`)
	}
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// ---------------------------------------------------------------------------
// fakeDoer：可编排的假 HTTP 传输，保证测试不发真实网络请求。

type fakeDoer struct {
	handler func(req *http.Request) (status int, body string)
	calls   []*http.Request
	bodies  []string
}

func (d *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, req)
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		body = string(raw)
	}
	d.bodies = append(d.bodies, body)
	status, reply := d.handler(req)
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(reply)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func base64URL(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func urlsOf(sources []Source) []string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = s.URL
	}
	return out
}

func assertURLSet(t *testing.T, got []Source, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", urlsOf(got), want)
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.URL] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("sources = %v, missing %q", urlsOf(got), w)
		}
	}
}

// ---------------------------------------------------------------------------
// 协议读取

func TestReadMessagesProtocol(t *testing.T) {
	read, err := ReadMessages([]byte(fixtureMessagesHit))
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if !read.Ran || read.Cut {
		t.Fatalf("read = %+v, want ran=true cut=false", read)
	}
	if len(read.Sources) != 1 {
		t.Fatalf("sources = %v, want 1 (dedupe + skip non-http)", urlsOf(read.Sources))
	}
	src := read.Sources[0]
	if src.URL != "https://a.example/" || src.Title != "A" || src.Snippet != "quoted excerpt" || src.PublishedAt != "2 days ago" {
		t.Fatalf("source = %+v", src)
	}

	empty, err := ReadMessages([]byte(fixtureMessagesNoSearch))
	if err != nil {
		t.Fatalf("ReadMessages(no search): %v", err)
	}
	if empty.Ran || len(empty.Sources) != 0 {
		t.Fatalf("no-search read = %+v", empty)
	}

	failed, err := ReadMessages([]byte(fixtureMessagesError))
	if err != nil {
		t.Fatalf("ReadMessages(error): %v", err)
	}
	if !strings.Contains(failed.Err, "max_uses_exceeded") {
		t.Fatalf("error read = %+v, want max_uses_exceeded", failed)
	}

	cut, err := ReadMessages([]byte(`{"content": [], "stop_reason": "max_tokens"}`))
	if err != nil || !cut.Cut {
		t.Fatalf("max_tokens read = %+v err=%v, want cut=true", cut, err)
	}
}

func TestReadResponsesProtocol(t *testing.T) {
	read, err := ReadResponses([]byte(fixtureResponsesHit))
	if err != nil {
		t.Fatalf("ReadResponses: %v", err)
	}
	if !read.Ran {
		t.Fatalf("read = %+v, want ran=true", read)
	}
	assertURLSet(t, read.Sources, "https://o.example/", "https://p.example/", "https://x.example/")
	for _, src := range read.Sources {
		if src.URL == "https://o.example/" && src.Title != "O" {
			t.Fatalf("o.example title = %q, want O (annotation fills action source)", src.Title)
		}
	}
	incomplete, err := ReadResponses([]byte(fixtureResponsesIncomplete))
	if err != nil || !incomplete.Cut || incomplete.Ran {
		t.Fatalf("incomplete read = %+v err=%v, want cut=true ran=false", incomplete, err)
	}
}

func TestReadGeminiProtocol(t *testing.T) {
	read, err := ReadGemini([]byte(fixtureGeminiHit))
	if err != nil {
		t.Fatalf("ReadGemini: %v", err)
	}
	if !read.Ran {
		t.Fatalf("read = %+v, want ran=true", read)
	}
	if len(read.Sources) != 1 || read.Sources[0].URL != "https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc" {
		t.Fatalf("sources = %+v", read.Sources)
	}
	cut, err := ReadGemini([]byte(fixtureGeminiMaxTokens))
	if err != nil || !cut.Cut || cut.Ran {
		t.Fatalf("max tokens read = %+v err=%v", cut, err)
	}
}

func TestFindSourcesAndReadDeclared(t *testing.T) {
	sources, err := FindSources([]byte(fixtureDeclared))
	if err != nil {
		t.Fatalf("FindSources: %v", err)
	}
	assertURLSet(t, sources, "https://ollama.example/", "https://l.example/") // 媒体链接与回显 tools 不算结果

	declared, err := ReadDeclared([]byte(fixtureDeclared))
	if err != nil || !declared.Ran || len(declared.Sources) != 2 {
		t.Fatalf("ReadDeclared = %+v err=%v", declared, err)
	}
	for _, body := range []string{`{"search_results": []}`, `{"results": []}`} {
		read, err := ReadDeclared([]byte(body))
		if err != nil || !read.Ran || len(read.Sources) != 0 {
			t.Fatalf("ReadDeclared(%s) = %+v err=%v, want ran=true empty", body, read, err)
		}
	}
	prose, err := ReadDeclared([]byte(`{"choices": [{"message": {"content": "see https://a.example/"}}]}`))
	if err != nil || prose.Ran {
		t.Fatalf("prose response = %+v err=%v, want ran=false", prose, err)
	}
}

// ---------------------------------------------------------------------------
// 无密钥兜底解析

func TestDecodeBingURL(t *testing.T) {
	encoded := base64URL("https://example.com/a?b=c")
	if got := DecodeBingURL("https://www.bing.com/ck/a?!&&p=x&u=a1" + encoded + "&ntb=1"); got != "https://example.com/a?b=c" {
		t.Fatalf("DecodeBingURL = %q", got)
	}
	if got := DecodeBingURL("https://www.bing.com/ck/a?u=a1" + base64URL("javascript:alert(1)")); got != "" {
		t.Fatalf("javascript target should be dropped, got %q", got)
	}
	if got := DecodeBingURL("https://example.com/plain"); got != "https://example.com/plain" {
		t.Fatalf("non-bing URL should pass through, got %q", got)
	}
	if got := DecodeBingURL("::not a url::"); got != "" {
		t.Fatalf("invalid URL should be empty, got %q", got)
	}
}

func TestParseBing(t *testing.T) {
	htmlText := bingFixture(
		base64URL("https://github.com/deepseek-ai/deepseek-harness/tree/master"),
		base64URL("https://example.com/second"),
	)
	parsed := ParseBing(htmlText)
	if parsed.Err != "" {
		t.Fatalf("ParseBing error = %q", parsed.Err)
	}
	assertURLSet(t, parsed.Sources, "https://github.com/deepseek-ai/deepseek-harness/tree/master", "https://example.com/second")
	if parsed.Sources[0].Title != "GitHub - deep/seek" {
		t.Fatalf("title = %q", parsed.Sources[0].Title)
	}
	if parsed.Sources[0].Snippet != "Snippet one & more …" {
		t.Fatalf("snippet = %q (entities/tags must be decoded)", parsed.Sources[0].Snippet)
	}

	noResults := ParseBing(`<html><body><div class="b_no">no results</div></body></html>`)
	if noResults.Err != "" || len(noResults.Sources) != 0 {
		t.Fatalf("no-results page = %+v", noResults)
	}
	botCheck := ParseBing(`<html><body>Please solve this challenge</body></html>`)
	if !strings.Contains(botCheck.Err, "not a results page") {
		t.Fatalf("bot-check page error = %q", botCheck.Err)
	}
}

func TestParseDuckDuckGo(t *testing.T) {
	htmlText := ddgFixture(
		url.QueryEscape("https://github.com/deepseek-ai/deepseek-harness"),
		url.QueryEscape("https://api-docs.deepseek.com/zh-cn/"),
	)
	parsed := ParseDuckDuckGo(htmlText)
	if parsed.Err != "" {
		t.Fatalf("ParseDuckDuckGo error = %q", parsed.Err)
	}
	assertURLSet(t, parsed.Sources, "https://github.com/deepseek-ai/deepseek-harness", "https://api-docs.deepseek.com/zh-cn/")
	if parsed.Sources[0].Title != "Result One" {
		t.Fatalf("title = %q (inline <b> must not split words)", parsed.Sources[0].Title)
	}
	if parsed.Sources[0].Snippet != "Snippet & more …" {
		t.Fatalf("snippet = %q", parsed.Sources[0].Snippet)
	}
	botCheck := ParseDuckDuckGo(`<p>Unfortunately, bots use DuckDuckGo too.</p>`)
	if botCheck.Err != "bot check page" {
		t.Fatalf("bot check error = %q", botCheck.Err)
	}
	noResults := ParseDuckDuckGo(`<html><body><div class="no-results">nothing</div></body></html>`)
	if noResults.Err != "" || len(noResults.Sources) != 0 {
		t.Fatalf("no-results = %+v", noResults)
	}
}

func TestTextOfAndEntities(t *testing.T) {
	if got := TextOf(`<strong>deep</strong>seek &amp; &#8230; &#x4e2d; &nbsp; done`); got != "deepseek & … 中 done" {
		t.Fatalf("TextOf = %q", got)
	}
	if got := TextOf(`<p>one</p><div>two<br>three</div>`); got != "one two three" {
		t.Fatalf("TextOf blocks = %q", got)
	}
	if got := DecodeEntities("&unknown; &amp;"); got != "&unknown; &" {
		t.Fatalf("DecodeEntities = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 候选规划（移植上游 planNative 断言）

func TestPlanNative(t *testing.T) {
	planURLs := func(route Route) []string {
		plan := PlanNative(route, nil)
		out := make([]string, len(plan.Candidates))
		for i, c := range plan.Candidates {
			out[i] = c.ID + " " + c.URL
		}
		return out
	}
	cases := []struct {
		name  string
		route Route
		want  []string
	}{
		{
			name:  "anthropic messages",
			route: Route{Provider: "r", Model: "m", API: "anthropic-messages", BaseURL: "https://api.deepseek.com/anthropic"},
			want:  []string{"messages https://api.deepseek.com/anthropic/v1/messages"},
		},
		{
			name:  "messages root already v1",
			route: Route{Provider: "r", Model: "m", API: "anthropic-messages", BaseURL: "https://gateway.example/v1/"},
			want:  []string{"messages https://gateway.example/v1/messages"},
		},
		{
			name:  "openai responses",
			route: Route{Provider: "r", Model: "m", API: "openai-responses", BaseURL: "https://api.openai.com/v1"},
			want:  []string{"responses https://api.openai.com/v1/responses"},
		},
		{
			name:  "gemini",
			route: Route{Provider: "r", Model: "gemini-2.5-flash", API: "google-generative-ai", BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
			want:  []string{"gemini https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"},
		},
		{
			name:  "openai completions tries responses then messages",
			route: Route{Provider: "r", Model: "m", API: "openai-completions", BaseURL: "https://api.together.ai/v1"},
			want:  []string{"responses https://api.together.ai/v1/responses", "messages https://api.together.ai/v1/messages"},
		},
		{
			name:  "unknown protocol",
			route: Route{Provider: "r", Model: "m", BaseURL: "http://127.0.0.1:8795"},
			want:  []string{"responses http://127.0.0.1:8795/responses", "messages http://127.0.0.1:8795/v1/messages"},
		},
		{
			name:  "declared api for route host comes first",
			route: Route{Provider: "r", Model: "m", API: "openai-completions", BaseURL: "https://ollama.com/v1"},
			want: []string{"ollama https://ollama.com/api/web_search", "responses https://ollama.com/v1/responses",
				"messages https://ollama.com/v1/messages"},
		},
		{
			name:  "kimi declared api",
			route: Route{Provider: "moonshotai-cn", Model: "kimi-k3", API: "openai-completions", BaseURL: "https://api.moonshot.cn/v1"},
			want: []string{"kimi https://api.moonshot.cn/v1/tools/search", "responses https://api.moonshot.cn/v1/responses",
				"messages https://api.moonshot.cn/v1/messages"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planURLs(tc.route)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("plan = %v, want %v", got, tc.want)
			}
		})
	}

	if plan := PlanNative(Route{Provider: "r", Model: "m", BaseURL: "not-a-url"}, nil); len(plan.Candidates) != 0 || !strings.Contains(plan.Reason, "no known endpoint") {
		t.Fatalf("bad base plan = %+v", plan)
	}
	if plan := PlanNative(Route{Provider: "r", BaseURL: "https://api.acme.example/v1", Model: "m", API: "weird-protocol"}, nil); len(plan.Candidates) != 0 || !strings.Contains(plan.Reason, "declares no search API") {
		t.Fatalf("no-shape plan = %+v", plan)
	}
	if plan := PlanNative(Route{Provider: "r", BaseURL: "https://api.acme.example/v1", API: "anthropic-messages"}, nil); len(plan.Candidates) != 0 || !strings.Contains(plan.Reason, "no current model") {
		t.Fatalf("no-model plan = %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// Searcher 编排（原生优先 + 兜底），全部走 fakeDoer

func bingFor() string {
	return bingFixture(base64URL("https://result.example/one"))
}

func TestSearcherNativeHit(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) {
		if strings.HasSuffix(req.URL.Path, "/messages") {
			return http.StatusOK, fixtureMessagesHit
		}
		return http.StatusNotFound, "{}"
	}}
	searcher := NewSearcher(SearcherOptions{
		Route: Route{Provider: "deepseek", Model: "deepseek-chat", API: "anthropic-messages", BaseURL: "https://api.example.com/anthropic", APIKey: "sk-test"},
		Doer:  doer,
	})
	result, err := searcher.Search(context.Background(), "golang generics")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Backend != "native: Anthropic Messages web_search tool" {
		t.Fatalf("backend = %q", result.Backend)
	}
	assertURLSet(t, result.Sources, "https://a.example/")
	if len(doer.calls) != 1 {
		t.Fatalf("native hit should make one call, got %d", len(doer.calls))
	}
	called := doer.calls[0]
	if called.URL.String() != "https://api.example.com/anthropic/v1/messages" {
		t.Fatalf("native URL = %s", called.URL)
	}
	if called.Header.Get("x-api-key") != "sk-test" || called.Header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth headers = x-api-key:%q authorization:%q", called.Header.Get("x-api-key"), called.Header.Get("Authorization"))
	}
	if !strings.Contains(doer.bodies[0], "web_search_20250305") || !strings.Contains(doer.bodies[0], "golang generics") {
		t.Fatalf("native request body = %s", doer.bodies[0])
	}
}

func TestSearcherFallbackAfterNativeFailure(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) {
		if strings.Contains(req.URL.Host, "bing.com") {
			return http.StatusOK, bingFor()
		}
		return http.StatusInternalServerError, "boom"
	}}
	searcher := NewSearcher(SearcherOptions{
		Route: Route{Provider: "acme", Model: "m", API: "anthropic-messages", BaseURL: "https://llm.acme.example/v1", APIKey: "k"},
		Doer:  doer,
	})
	result, err := searcher.Search(context.Background(), "query")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Backend != "fallback: Bing" {
		t.Fatalf("backend = %q", result.Backend)
	}
	assertURLSet(t, result.Sources, "https://result.example/one")
}

func TestSearcherFallbackEmptyResults(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) {
		return http.StatusOK, `<html><body><div class="b_no">no results</div></body></html>`
	}}
	searcher := NewSearcher(SearcherOptions{
		Route:         Route{Provider: "acme", BaseURL: "https://llm.acme.example/v1"},
		Doer:          doer,
		DisableNative: true,
	})
	result, err := searcher.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Backend != "fallback: Bing" || len(result.Sources) != 0 {
		t.Fatalf("empty-result search = %+v", result)
	}
}

func TestSearcherUnavailable(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) { return http.StatusInternalServerError, "boom" }}
	searcher := NewSearcher(SearcherOptions{
		Route: Route{Provider: "acme", Model: "m", API: "anthropic-messages", BaseURL: "https://llm.acme.example/v1", APIKey: "k"},
		Doer:  doer,
	})
	_, err := searcher.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("expected SearchUnavailableError")
	}
	var unavailable *SearchUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error type = %T, want *SearchUnavailableError", err)
	}
	if !strings.Contains(err.Error(), "Do not guess URLs") {
		t.Fatalf("error = %v, want anti-hallucination instruction", err)
	}
}

func TestSearcherGuardsAndDisableNative(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) { return http.StatusOK, bingFor() }}
	searcher := NewSearcher(SearcherOptions{
		Route:         Route{Provider: "acme", BaseURL: "https://llm.acme.example/v1"},
		Doer:          doer,
		DisableNative: true,
	})
	result, err := searcher.Search(context.Background(), "q")
	if err != nil || result.Backend != "fallback: Bing" {
		t.Fatalf("native-disabled search = %+v err=%v", result, err)
	}

	if _, err := NewSearcher(SearcherOptions{}).Search(context.Background(), "q"); err == nil || !strings.Contains(err.Error(), "no HTTPDoer") {
		t.Fatalf("nil doer error = %v", err)
	}
	if _, err := NewSearcher(SearcherOptions{Doer: doer}).Search(context.Background(), "   "); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestSearcherDeclaredAPI(t *testing.T) {
	doer := &fakeDoer{handler: func(req *http.Request) (int, string) {
		if req.URL.Path == "/api/web_search" {
			return http.StatusOK, `{"results": [{"title": "Ollama", "url": "https://o.example/", "content": "snippet"}]}`
		}
		return http.StatusNotFound, "{}"
	}}
	searcher := NewSearcher(SearcherOptions{
		Route: Route{Provider: "ollama", Model: "m", API: "openai-completions", BaseURL: "https://ollama.com/v1"},
		Doer:  doer,
	})
	result, err := searcher.Search(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Backend != "native: Ollama web search API" {
		t.Fatalf("backend = %q", result.Backend)
	}
	assertURLSet(t, result.Sources, "https://o.example/")
	if len(doer.bodies) == 0 || !strings.Contains(doer.bodies[0], `"query":"golang"`) {
		t.Fatalf("declared API body = %v", doer.bodies)
	}
}

// ---------------------------------------------------------------------------
// 插件门面：IM2A LLM_* 环境变量 → Searcher（loopback httptest，无外网）

func TestPluginWebSearchViaLLMEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fixtureResponsesHit)
	}))
	defer srv.Close()

	t.Setenv("LLM_BASE_URL", srv.URL+"/v1")
	t.Setenv("LLM_MODEL", "m")
	t.Setenv("LLM_API_KEY", "sk-env")
	t.Setenv(EnvEndpoint, "")

	p := New(Deps{Enabled: true})
	result, err := p.WebSearch(context.Background(), "q")
	if err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	if !strings.HasPrefix(result.Backend, "native: ") {
		t.Fatalf("backend = %q", result.Backend)
	}
	if len(result.Sources) == 0 {
		t.Fatal("expected sources from loopback fixture server")
	}
}
