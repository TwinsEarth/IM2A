// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

// repoRoot 返回仓库根（含 go.mod 的目录）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", file)
		}
		dir = parent
	}
}

// readRepoFile 读取仓库内相对路径文件。
func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

// testKeyPair 返回确定性测试密钥对（只用于测试，生产私钥不落仓库）。
func testKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := sha256.Sum256([]byte("im2a-plugin-runtime-test-key-v1"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("ed25519 public key type assertion failed")
	}
	return pub, priv
}

// testKeyID 是测试公钥的 keyId（符合 manifest publisher.keyId 规则）。
const testKeyID = "test-key-01"

// baseManifestMap 构造一份最小合法 manifest（kind 决定 entrypoint）。
func baseManifestMap(id, version string, kind Kind) map[string]any {
	ep := map[string]any{"type": "inprocess", "module": "human-agent-im/internal/plugin/" + id}
	if kind == KindK1 {
		ep = map[string]any{"type": "exec", "path": "bin/" + id, "args": []any{"--serve"}}
	}
	if kind == KindK2 {
		ep = map[string]any{"type": "remote", "url": "wss://plugins.example/" + id}
	}
	return map[string]any{
		"apiVersion": APIVersion,
		"id":         id,
		"name":       "Test " + id,
		"version":    version,
		"kind":       string(kind),
		"api":        CapabilityAPI,
		"publisher":  map[string]any{"id": "im2a-test", "keyId": testKeyID},
		"entrypoint": ep,
		"provides": []any{
			map[string]any{"id": "test." + id + ".cap", "version": "v1"},
		},
		"permissions": []any{},
		"attribution": map[string]any{
			"license":   "MIT",
			"copyright": "Copyright (c) 2026 Test",
			"upstream":  "https://example.com/upstream",
			"derived":   false,
			"usage":     []any{"test fixture"},
		},
	}
}

// mustJSON 把 map 序列化为 JSON；失败直接 Fatal。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// hasIssueCode 判断校验错误中是否包含指定 code。
func hasIssueCode(err error, code string) bool {
	ve, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	for _, is := range ve.Issues {
		if is.Code == code {
			return true
		}
	}
	return false
}
