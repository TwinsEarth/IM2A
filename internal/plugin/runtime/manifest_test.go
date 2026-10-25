// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"errors"
	"strings"
	"testing"
)

// 内建 K0 manifest 必须通过完整校验，且 K0 的 security_boundary 恒为 false。
func TestBundledManifestsValid(t *testing.T) {
	items, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 bundled manifests, got %d", len(items))
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.Manifest.ID] = true
		if it.Manifest.Kind != KindK0 {
			t.Errorf("%s kind = %s, want K0", it.Manifest.ID, it.Manifest.Kind)
		}
		if it.Signature != SigNotRequired {
			t.Errorf("%s signature = %s, want not_required", it.Manifest.ID, it.Signature)
		}
		if !strings.HasPrefix(it.ManifestHash, "sha256:") {
			t.Errorf("%s hash = %q, want sha256: prefix", it.Manifest.ID, it.ManifestHash)
		}
		if it.SecurityBoundary() {
			t.Errorf("%s: K0 must NOT claim a security boundary", it.Manifest.ID)
		}
	}
	if !ids["openclaw"] || !ids["dsbot"] {
		t.Fatalf("bundled ids = %v, want openclaw+dsbot", ids)
	}
}

// 部署形态 openclaw manifest 必须能通过完整校验（真实 manifest，不是占位）。
func TestDeployOpenClawManifestValid(t *testing.T) {
	raw := readRepoFile(t, "deploy/plugins/openclaw/manifest.json")
	m, hash, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse deploy openclaw manifest: %v", err)
	}
	if m.ID != "openclaw" || m.Version != "1.0.0" || m.Kind != KindK0 {
		t.Fatalf("unexpected manifest: id=%s version=%s kind=%s", m.ID, m.Version, m.Kind)
	}
	if len(m.Provides) != 4 {
		t.Fatalf("openclaw provides = %d, want 4", len(m.Provides))
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash = %q", hash)
	}
	if SecurityBoundary(m.Kind) {
		t.Fatal("K0 openclaw must have security_boundary=false")
	}
}

// 未知 kind / 未知字段 / 缺必填 / 类型错 / 未知权限类 / 空 scope：全部具名错误。
func TestManifestValidationNamedErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m map[string]any)
		wantErr string
	}{
		{"unknown kind", func(m map[string]any) { m["kind"] = "K9" }, CodeInvalidKind},
		{"missing kind", func(m map[string]any) { delete(m, "kind") }, CodeSchemaMissing},
		{"bad id", func(m map[string]any) { m["id"] = "Bad_ID" }, CodeInvalidID},
		{"bad version", func(m map[string]any) { m["version"] = "1.0" }, CodeInvalidVersion},
		{"missing permissions", func(m map[string]any) { delete(m, "permissions") }, CodeSchemaMissing},
		{"bad permission class", func(m map[string]any) {
			m["permissions"] = []any{map[string]any{"class": "root.all", "scope": []any{"*"}}}
		}, CodeInvalidPermission},
		{"empty permission scope", func(m map[string]any) {
			m["permissions"] = []any{map[string]any{"class": "exec", "scope": []any{}}}
		}, CodeInvalidPermission},
		{"missing attribution", func(m map[string]any) { delete(m, "attribution") }, CodeSchemaMissing},
		{"empty provides", func(m map[string]any) { m["provides"] = []any{} }, CodeInvalidProvides},
		{"duplicate capability", func(m map[string]any) {
			m["provides"] = []any{
				map[string]any{"id": "a.b", "version": "v1"},
				map[string]any{"id": "a.b", "version": "v1"},
			}
		}, CodeDuplicateCapability},
		{"K1 with inprocess", func(m map[string]any) {
			m["kind"] = "K1"
			m["entrypoint"] = map[string]any{"type": "inprocess", "module": "x"}
		}, CodeInvalidEntrypoint},
		{"K1 unsafe path", func(m map[string]any) {
			m["kind"] = "K1"
			m["entrypoint"] = map[string]any{"type": "exec", "path": "../escape"}
		}, CodeInvalidEntrypoint},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := baseManifestMap("err-case", "1.0.0", KindK1)
			c.mutate(m)
			_, _, err := Parse(mustJSON(t, m))
			if err == nil {
				t.Fatalf("want error %s, got nil", c.wantErr)
			}
			if !hasIssueCode(err, c.wantErr) {
				t.Fatalf("want issue code %s, got: %v", c.wantErr, err)
			}
		})
	}
}

// 未知顶层字段必须拒绝（非 x-* 扩展），防止"安全字段被静默忽略"。
func TestParseRejectsUnknownField(t *testing.T) {
	m := baseManifestMap("unknown-field", "1.0.0", KindK1)
	m["permissionz"] = []any{} // 拼写错误：不能被静默忽略
	_, _, err := Parse(mustJSON(t, m))
	if !hasIssueCode(err, CodeUnknownField) {
		t.Fatalf("want %s, got %v", CodeUnknownField, err)
	}
}

// x-* 扩展字段允许（必须进入 canonical 哈希，签名覆盖它们）。
func TestParseAllowsXExtensionsAndHashesThem(t *testing.T) {
	m := baseManifestMap("ext-test", "1.0.0", KindK1)
	rawA := mustJSON(t, m)
	m["x-note"] = "hello"
	rawB := mustJSON(t, m)
	_, hashA, err := Parse(rawA)
	if err != nil {
		t.Fatalf("parse A: %v", err)
	}
	parsed, hashB, err := Parse(rawB)
	if err != nil {
		t.Fatalf("parse B: %v", err)
	}
	if _, ok := parsed.Extensions["x-note"]; !ok {
		t.Fatalf("x-note extension not captured: %v", parsed.Extensions)
	}
	if hashA == hashB {
		t.Fatal("changing an x-* extension must change manifest_hash (signature covers it)")
	}
}

// 重复 JSON key 必须拒绝。
func TestParseRejectsDuplicateJSONKey(t *testing.T) {
	raw := []byte(`{"apiVersion":"im2a.plugin/v1","id":"a","id":"b"}`)
	if _, _, err := Parse(raw); !hasIssueCode(err, CodeSchemaInvalid) {
		t.Fatalf("want %s, got %v", CodeSchemaInvalid, err)
	}
}

// 坏类型（provides 不是数组）必须在解码期具名拒绝。
func TestParseRejectsBadType(t *testing.T) {
	m := baseManifestMap("bad-type", "1.0.0", KindK1)
	m["provides"] = "not-an-array"
	if _, _, err := Parse(mustJSON(t, m)); !hasIssueCode(err, CodeSchemaInvalid) {
		t.Fatalf("want %s, got %v", CodeSchemaInvalid, err)
	}
}

// SecurityBoundary 是硬裁决：K0 false，K1/K2 true；不允许 K0 通过任何 manifest 内容变 true。
func TestSecurityBoundaryHardRule(t *testing.T) {
	if SecurityBoundary(KindK0) {
		t.Fatal("K0 must never claim a security boundary")
	}
	if !SecurityBoundary(KindK1) || !SecurityBoundary(KindK2) {
		t.Fatal("K1/K2 are the only kinds that can have a security boundary")
	}
	loaded := &Loaded{Manifest: &Manifest{Kind: KindK0}}
	if loaded.SecurityBoundary() {
		t.Fatal("Loaded(K0).SecurityBoundary must be false")
	}
	if (&Loaded{Manifest: &Manifest{Kind: KindK1}}).SecurityBoundary() != true {
		t.Fatal("Loaded(K1).SecurityBoundary must be true")
	}
}

// minKernelVersion / targets 的语义错误必须具名。
func TestManifestVersionAndPlatformValidation(t *testing.T) {
	m := baseManifestMap("ver-check", "1.0.0", KindK1)
	m["minKernelVersion"] = "not-a-version"
	_, _, err := Parse(mustJSON(t, m))
	if !hasIssueCode(err, CodeInvalidVersion) {
		t.Fatalf("want %s, got %v", CodeInvalidVersion, err)
	}

	m2 := baseManifestMap("target-check", "1.0.0", KindK1)
	m2["targets"] = []any{"solaris/sparc"}
	_, _, err = Parse(mustJSON(t, m2))
	if err == nil {
		t.Fatal("invalid target must be rejected")
	}
}

// ValidationError 必须可被 errors.Is 识别为 ErrInvalidManifest。
func TestValidationErrorIs(t *testing.T) {
	_, _, err := Parse([]byte(`{}`))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
}
