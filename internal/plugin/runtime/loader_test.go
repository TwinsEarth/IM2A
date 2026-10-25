// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInstalled 在临时插件根下写入一个 K1 manifest（可选签名）。
func writeInstalled(t *testing.T, root, id, version string, kind Kind, mutate func(map[string]any), sign bool) string {
	t.Helper()
	m := baseManifestMap(id, version, kind)
	if mutate != nil {
		mutate(m)
	}
	dir := filepath.Join(root, id, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw := mustJSON(t, m)
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if sign {
		_, priv := testKeyPair(t)
		sig, err := SignManifest(priv, raw)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.sig"), sig, 0o644); err != nil {
			t.Fatalf("write sig: %v", err)
		}
	}
	return dir
}

// testLoaderOptions 返回带测试信任库、固定平台的 Options。
func testLoaderOptions(t *testing.T) Options {
	t.Helper()
	pub, _ := testKeyPair(t)
	ts := NewTrustStore()
	if err := ts.Add(testKeyID, pub); err != nil {
		t.Fatal(err)
	}
	return Options{Trust: ts, Platform: "linux/amd64"}
}

// 拓扑：c → a → b（b requires a，a requires c）→ 启动顺序固定 c,a,b。
func TestBuildResultTopoOrder(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "pkg-c", "1.0.0", KindK1, nil, true)
	writeInstalled(t, root, "pkg-a", "1.0.0", KindK1, func(m map[string]any) {
		m["requires_plugins"] = []any{"pkg-c"}
	}, true)
	writeInstalled(t, root, "pkg-b", "1.0.0", KindK1, func(m map[string]any) {
		m["requires_plugins"] = []any{"pkg-a"}
	}, true)

	opts := testLoaderOptions(t)
	items, _, err := LoadInstalled(root, opts)
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	res, err := BuildResult(items, opts)
	if err != nil {
		t.Fatalf("BuildResult: %v", err)
	}
	want := []string{"pkg-c", "pkg-a", "pkg-b"}
	if strings.Join(res.StartupOrder(), ",") != strings.Join(want, ",") {
		t.Fatalf("startup order = %v, want %v", res.StartupOrder(), want)
	}
}

// 能力依赖：唯一 provider 构成硬边（prov 先于 cons）。
func TestBuildResultCapabilityEdge(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "prov", "1.0.0", KindK1, func(m map[string]any) {
		m["provides"] = []any{map[string]any{"id": "svc.data", "version": "v1"}}
	}, true)
	writeInstalled(t, root, "cons", "1.0.0", KindK1, func(m map[string]any) {
		m["requires"] = []any{map[string]any{"capability": "svc.data", "version": "v1"}}
	}, true)

	opts := testLoaderOptions(t)
	items, _, err := LoadInstalled(root, opts)
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	res, err := BuildResult(items, opts)
	if err != nil {
		t.Fatalf("BuildResult: %v", err)
	}
	if got := res.StartupOrder(); strings.Join(got, ",") != "prov,cons" {
		t.Fatalf("startup order = %v, want [prov cons]", got)
	}
}

// 环检测：a↔b 必须返回具名 E_DEP_CYCLE（含可读路径）。
func TestBuildResultCycleDetected(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "ca", "1.0.0", KindK1, func(m map[string]any) { m["requires_plugins"] = []any{"cb"} }, true)
	writeInstalled(t, root, "cb", "1.0.0", KindK1, func(m map[string]any) { m["requires_plugins"] = []any{"ca"} }, true)

	opts := testLoaderOptions(t)
	items, _, err := LoadInstalled(root, opts)
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	_, err = BuildResult(items, opts)
	var le *LoadError
	if !errors.As(err, &le) || le.Code != CodeDependencyCycle {
		t.Fatalf("want %s, got %v", CodeDependencyCycle, err)
	}
	if !strings.Contains(le.Path, "->") {
		t.Fatalf("cycle path not readable: %q", le.Path)
	}
}

// 缺失必需能力：默认标记 blocked（不启动），StrictRequired 时直接报错。
func TestBuildResultMissingRequired(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "needy", "1.0.0", KindK1, func(m map[string]any) {
		m["requires"] = []any{map[string]any{"capability": "nobody.provides.this", "version": "v1"}}
	}, true)
	opts := testLoaderOptions(t)
	items, _, err := LoadInstalled(root, opts)
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	res, err := BuildResult(items, opts)
	if err != nil {
		t.Fatalf("non-strict BuildResult: %v", err)
	}
	it, ok := res.Get("needy")
	if !ok {
		t.Fatal("needy not found")
	}
	if it.State != StateBlocked {
		t.Fatalf("state = %s, want blocked", it.State)
	}
	if len(res.StartupOrder()) != 0 {
		t.Fatalf("blocked plugin must not be in startup order: %v", res.StartupOrder())
	}
	if !hasIssueIn(res.Issues, CodeDependencyMissing) {
		t.Fatalf("missing issue code %s: %+v", CodeDependencyMissing, res.Issues)
	}

	strict := opts
	strict.StrictRequired = true
	if _, err := BuildResult(items, strict); err == nil {
		t.Fatal("strict mode must reject missing required capability")
	} else {
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeDependencyMissing {
			t.Fatalf("want %s, got %v", CodeDependencyMissing, err)
		}
	}
}

// 可选依赖缺失：degraded 但仍启动，并给告警码。
func TestBuildResultOptionalMissingDegraded(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "optional", "1.0.0", KindK1, func(m map[string]any) {
		m["optional_requires"] = []any{map[string]any{"capability": "optional.missing", "version": "v1"}}
	}, true)
	opts := testLoaderOptions(t)
	items, _, err := LoadInstalled(root, opts)
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	res, err := BuildResult(items, opts)
	if err != nil {
		t.Fatalf("BuildResult: %v", err)
	}
	it, _ := res.Get("optional")
	if it.State != StateDegraded {
		t.Fatalf("state = %s, want degraded", it.State)
	}
	if len(res.StartupOrder()) != 1 || res.StartupOrder()[0] != "optional" {
		t.Fatalf("degraded plugin must still start: %v", res.StartupOrder())
	}
	if !hasIssueIn(res.Issues, WarnOptionalDepMissing) {
		t.Fatalf("missing warning %s: %+v", WarnOptionalDepMissing, res.Issues)
	}
}

// K0 不允许从磁盘安装（防止伪装内建插件）。
func TestLoadInstalledRejectsK0OnDisk(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "fake-builtin", "1.0.0", KindK0, nil, false)
	_, _, err := LoadInstalled(root, testLoaderOptions(t))
	var le *LoadError
	if !errors.As(err, &le) || le.Code != CodeK0OnDisk {
		t.Fatalf("want %s, got %v", CodeK0OnDisk, err)
	}
}

// 签名四态在 loader 层同样具名拒绝。
func TestLoadInstalledSignatureStates(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		writeInstalled(t, root, "unsigned", "1.0.0", KindK1, nil, false)
		_, _, err := LoadInstalled(root, testLoaderOptions(t))
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeSignatureMissing || !errors.Is(err, ErrSignatureMissing) {
			t.Fatalf("want %s, got %v", CodeSignatureMissing, err)
		}
	})
	t.Run("bad", func(t *testing.T) {
		root := t.TempDir()
		dir := writeInstalled(t, root, "badsig", "1.0.0", KindK1, nil, true)
		sigPath := filepath.Join(dir, "manifest.sig")
		sig, _ := os.ReadFile(sigPath)
		sig[0] ^= 0xFF
		_ = os.WriteFile(sigPath, sig, 0o644)
		_, _, err := LoadInstalled(root, testLoaderOptions(t))
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeBadSignature || !errors.Is(err, ErrBadSignature) {
			t.Fatalf("want %s, got %v", CodeBadSignature, err)
		}
	})
	t.Run("unknown-key", func(t *testing.T) {
		root := t.TempDir()
		dir := writeInstalled(t, root, "unknownkey", "1.0.0", KindK1, func(m map[string]any) {
			m["publisher"] = map[string]any{"id": "im2a-test", "keyId": "unknown-key-99"}
		}, false)
		// 先用测试私钥签（keyId 不被信任库包含）。
		raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
		_, priv := testKeyPair(t)
		sig, err := SignManifest(priv, raw)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), sig, 0o644)
		_, _, err = LoadInstalled(root, testLoaderOptions(t))
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeUnknownPublisherKey || !errors.Is(err, ErrUnknownPublisherKey) {
			t.Fatalf("want %s, got %v", CodeUnknownPublisherKey, err)
		}
	})
}

// 平台不匹配必须拒绝（防把 linux 二进制装到别处）。
func TestLoadInstalledPlatformMismatch(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "wrongplat", "1.0.0", KindK1, func(m map[string]any) {
		m["targets"] = []any{"windows/amd64"}
	}, true)
	_, _, err := LoadInstalled(root, testLoaderOptions(t))
	var le *LoadError
	if !errors.As(err, &le) || le.Code != CodeUnsupportedPlatform {
		t.Fatalf("want %s, got %v", CodeUnsupportedPlatform, err)
	}
}

// 多版本共存：选最高版本，其余给告警。
func TestLoadInstalledPicksHighestVersion(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "multi", "1.0.0", KindK1, nil, true)
	writeInstalled(t, root, "multi", "1.1.0", KindK1, nil, true)
	items, issues, err := LoadInstalled(root, testLoaderOptions(t))
	if err != nil {
		t.Fatalf("LoadInstalled: %v", err)
	}
	if len(items) != 1 || items[0].Manifest.Version != "1.1.0" {
		t.Fatalf("want highest 1.1.0, got %+v", items)
	}
	if !hasIssueIn(issues, WarnMultipleVersions) {
		t.Fatalf("missing %s: %+v", WarnMultipleVersions, issues)
	}
}

// Loader.Load = 内建 K0 + 目录安装；JSON 输出必须带 security_boundary/manifest_hash/签名状态。
func TestLoaderLoadAndJSON(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "k1-demo", "1.0.0", KindK1, nil, true)
	l := NewLoader(root, testLoaderOptions(t))
	res, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("want 3 plugins (2 bundled + 1 installed), got %d", len(res.Items))
	}
	// 启动顺序含三者；同一层按 id 字典序。
	order := strings.Join(res.StartupOrder(), ",")
	if order != "dsbot,k1-demo,openclaw" && order != "dsbot,openclaw,k1-demo" {
		t.Fatalf("unexpected startup order: %s", order)
	}

	rawJSON, err := res.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var out struct {
		Plugins []struct {
			ID               string `json:"id"`
			SecurityBoundary bool   `json:"security_boundary"`
			ManifestHash     string `json:"manifest_hash"`
			Signature        string `json:"signature"`
		} `json:"plugins"`
		StartupOrder []string `json:"startup_order"`
	}
	if err := json.Unmarshal(rawJSON, &out); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	byID := map[string]struct {
		sb, sig, hash string
	}{}
	for _, p := range out.Plugins {
		byID[p.ID] = struct{ sb, sig, hash string }{boolStr(p.SecurityBoundary), p.Signature, p.ManifestHash}
	}
	oc, ok := byID["openclaw"]
	if !ok {
		t.Fatalf("openclaw missing from JSON: %s", rawJSON)
	}
	if oc.sb != "false" {
		t.Fatalf("K0 security_boundary must be false, got %s", oc.sb)
	}
	if oc.sig != string(SigNotRequired) {
		t.Fatalf("K0 signature = %s, want %s", oc.sig, SigNotRequired)
	}
	if !strings.HasPrefix(oc.hash, "sha256:") {
		t.Fatalf("manifest_hash = %q", oc.hash)
	}
	k1 := byID["k1-demo"]
	if k1.sb != "true" {
		t.Fatalf("K1 security_boundary must be true, got %s", k1.sb)
	}
	if k1.sig != string(SigValid) {
		t.Fatalf("K1 signature = %s, want valid", k1.sig)
	}
}

// 重复 plugin id 必须拒绝。
func TestBuildResultDuplicatePlugin(t *testing.T) {
	m := &Manifest{ID: "dup", Kind: KindK0, Provides: []Capability{{ID: "dup.cap", Version: "v1"}}}
	items := []*Loaded{
		{Manifest: m, State: StateOK},
		{Manifest: m, State: StateOK},
	}
	if _, err := BuildResult(items, Options{}); err == nil {
		t.Fatal("duplicate plugin id must be rejected")
	} else {
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeDuplicatePlugin {
			t.Fatalf("want %s, got %v", CodeDuplicatePlugin, err)
		}
	}
}

func hasIssueIn(issues []Issue, code string) bool {
	for _, is := range issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
