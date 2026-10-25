// SPDX-License-Identifier: MIT

package pluginruntime

// JSON canonical 化（IM2A canonical v1）与 manifest_hash。
//
// 规则（施工口径，测试锁定）：
//   - 只接受合法 UTF-8 的单个 JSON 对象；拒绝重复 key、拒绝尾随数据；
//   - 对象 key 按字节序排序；数组保序；
//   - 字符串用 Go encoding/json 的确定性转义（HTML 字符会被转义，跨进程一致）；
//   - 数字归一化：整数值按最小十进制输出（300、-5、0），其他按最短浮点表示；
//   - 不输出任何空白（分隔符仅 ',' ':'）。
//
// 为什么不用 typed-struct 重编码：canonical 必须覆盖 x-* 扩展字段与未来新增字段，
// 否则"签名覆盖范围"会随结构体演进而漂移。这里是**先严格解析再规范化输出**，
// 同一语义的任意合法 JSON（键序/空白/数字写法不同）都收敛到同一字节串。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrCanonical 是 canonical 化失败的错误基类（具体原因在 message 里）。
var ErrCanonical = errors.New("plugin runtime: canonical json")

// Canonicalize 把任意合法 JSON 对象转为 canonical 字节串。
func Canonicalize(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrCanonical)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	out, err := decodeCanonicalValue(dec, "$")
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data after top-level value", ErrCanonical)
	}
	// 顶层必须是对象：manifest 是对象。
	if len(out) == 0 || out[0] != '{' {
		return nil, fmt.Errorf("%w: top-level value must be an object", ErrCanonical)
	}
	return out, nil
}

// Hash 返回 canonical JSON 的 manifest_hash（"sha256:<hex>"）。
func Hash(raw []byte) (string, error) {
	c, err := Canonicalize(raw)
	if err != nil {
		return "", err
	}
	return HashCanonical(c), nil
}

// HashCanonical 对已经是 canonical 的字节串求 manifest_hash。
func HashCanonical(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// decodeCanonicalValue 递归解码一个值并输出 canonical 字节。
func decodeCanonicalValue(dec *json.Decoder, path string) ([]byte, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCanonical, path, err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeCanonicalObject(dec, path)
		case '[':
			return decodeCanonicalArray(dec, path)
		default:
			return nil, fmt.Errorf("%w: %s: unexpected %q", ErrCanonical, path, string(t))
		}
	case string:
		return json.Marshal(t) // 确定性转义
	case json.Number:
		return canonicalNumber(t, path)
	case bool:
		if t {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case nil:
		return []byte("null"), nil
	default:
		return nil, fmt.Errorf("%w: %s: unsupported token %T", ErrCanonical, path, tok)
	}
}

func decodeCanonicalObject(dec *json.Decoder, path string) ([]byte, error) {
	type pair struct{ k, v string }
	seen := make(map[string]bool, 8)
	pairs := make([]pair, 0, 8)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: object key: %v", ErrCanonical, path, err)
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s: object key is not a string", ErrCanonical, path)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: %s: duplicate key %q", ErrCanonical, path, key)
		}
		seen[key] = true
		v, err := decodeCanonicalValue(dec, path+"."+key)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, pair{k: key, v: string(v)})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, fmt.Errorf("%w: %s: close object: %v", ErrCanonical, path, err)
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(p.k)
		b.Write(kb)
		b.WriteByte(':')
		b.WriteString(p.v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func decodeCanonicalArray(dec *json.Decoder, path string) ([]byte, error) {
	items := make([]string, 0, 4)
	i := 0
	for dec.More() {
		v, err := decodeCanonicalValue(dec, path+"["+strconv.Itoa(i)+"]")
		if err != nil {
			return nil, err
		}
		items = append(items, string(v))
		i++
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, fmt.Errorf("%w: %s: close array: %v", ErrCanonical, path, err)
	}
	var b strings.Builder
	b.WriteByte('[')
	for j, it := range items {
		if j > 0 {
			b.WriteByte(',')
		}
		b.WriteString(it)
	}
	b.WriteByte(']')
	return []byte(b.String()), nil
}

var integerLiteral = func() func(string) bool {
	return func(s string) bool {
		if s == "" {
			return false
		}
		if s[0] == '-' {
			s = s[1:]
		}
		if s == "0" {
			return true
		}
		if s == "" || s[0] < '1' || s[0] > '9' {
			return false
		}
		for i := 1; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
}()

// canonicalNumber 归一化 JSON 数字。manifest 里的整数优先按整数输出；
// 浮点（如 rollback.autoRollback.errorRate）按最短表示输出，避免 0.050 与 0.05 哈希不同。
func canonicalNumber(n json.Number, path string) ([]byte, error) {
	s := n.String()
	if integerLiteral(s) {
		// big.Int 语义的规范化：-0 → 0。
		if s == "-0" {
			return []byte("0"), nil
		}
		return []byte(s), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, fmt.Errorf("%w: %s: invalid number %q", ErrCanonical, path, s)
	}
	if f == math.Trunc(f) && math.Abs(f) <= 1<<53 {
		return []byte(strconv.FormatFloat(f, 'f', -1, 64)), nil
	}
	return []byte(strconv.FormatFloat(f, 'g', -1, 64)), nil
}
