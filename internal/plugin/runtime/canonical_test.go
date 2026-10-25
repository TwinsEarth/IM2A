// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// 键序/空白不同 → canonical 字节与 hash 必须相同（否则签名会被格式漂移破坏）。
func TestCanonicalKeyOrderAndWhitespace(t *testing.T) {
	a := []byte(`{"b":1,"a":{"y":2,"x":[1,2,{"d":4,"c":3}]}}`)
	b := []byte("{\n  \"a\": {\"x\": [1, 2, {\"c\": 3, \"d\": 4}], \"y\": 2},\n  \"b\": 1\n}\n")
	ca, err := Canonicalize(a)
	if err != nil {
		t.Fatalf("canonicalize a: %v", err)
	}
	cb, err := Canonicalize(b)
	if err != nil {
		t.Fatalf("canonicalize b: %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("canonical mismatch:\n a=%s\n b=%s", ca, cb)
	}
	ha, _ := Hash(a)
	hb, _ := Hash(b)
	if ha != hb {
		t.Fatalf("hash mismatch: %s != %s", ha, hb)
	}
}

// 数字写法不同但语义相同 → 同一 canonical（0.050 vs 0.05；300.0 vs 300）。
func TestCanonicalNumberNormalization(t *testing.T) {
	a := []byte(`{"rate":0.050,"n":300.0,"m":-0}`)
	b := []byte(`{"n":300,"rate":0.05,"m":0}`)
	ha, err := Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("number normalization mismatch: %s != %s", ha, hb)
	}
}

// 重复 key 必须拒绝（否则签名覆盖范围可被"后写覆盖前写"绕过）。
func TestCanonicalRejectsDuplicateKey(t *testing.T) {
	_, err := Hash([]byte(`{"id":"a","id":"b"}`))
	if !errors.Is(err, ErrCanonical) || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("want duplicate key error, got %v", err)
	}
}

// 非对象顶层/非法 JSON/尾随数据/非法 UTF-8 都必须拒绝。
func TestCanonicalRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"array top":    []byte(`[1,2]`),
		"trailing":     []byte(`{"a":1} {"b":2}`),
		"invalid utf8": append([]byte(`{"a":"`), 0xff, '"', '}'),
		"bad json":     []byte(`{"a":`),
	}
	for name, raw := range cases {
		if _, err := Canonicalize(raw); !errors.Is(err, ErrCanonical) {
			t.Errorf("%s: want ErrCanonical, got %v", name, err)
		}
	}
}

// 核心要求：canonical 字节串任一字节变化，manifest_hash 必变（或直接解析失败，绝不相同）。
func TestHashChangesOnAnyByteMutation(t *testing.T) {
	raw := readRepoFile(t, "deploy/plugins/openclaw/manifest.json")
	canonical, err := Canonicalize(raw)
	if err != nil {
		t.Fatalf("canonicalize openclaw: %v", err)
	}
	base := HashCanonical(canonical)
	if len(canonical) < 128 {
		t.Fatalf("canonical too short: %d", len(canonical))
	}
	for i := range canonical {
		mutated := append([]byte(nil), canonical...)
		mutated[i] ^= 0x01
		got, err := Hash(mutated)
		if err != nil {
			continue // 解析失败也是"不同"，绝不允许返回同一 hash
		}
		if got == base {
			t.Fatalf("hash unchanged after mutating canonical byte %d (%q)", i, canonical[i])
		}
	}
}

// 空输入与 nil 的安全行为。
func TestCanonicalEmptyInput(t *testing.T) {
	if _, err := Canonicalize(nil); err == nil {
		t.Fatal("nil input must fail")
	}
}
