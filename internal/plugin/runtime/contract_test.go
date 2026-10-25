// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dsbotplugin "human-agent-im/internal/plugin/dsbot"
	openclawplugin "human-agent-im/internal/plugin/openclaw"
)

// openclaw 真实 manifest 必须与 Go 插件代码一致（防止 manifest 与实现漂移）。
func TestOpenClawManifestMatchesPluginCode(t *testing.T) {
	items, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}
	var m *Manifest
	for _, it := range items {
		if it.Manifest.ID == openclawplugin.PluginID {
			m = it.Manifest
		}
	}
	if m == nil {
		t.Fatal("bundled openclaw manifest missing")
	}
	if m.Version != openclawplugin.PluginVersion {
		t.Errorf("manifest version %s != plugin.PluginVersion %s", m.Version, openclawplugin.PluginVersion)
	}
	if m.Kind != KindK0 {
		t.Errorf("openclaw kind = %s, want K0", m.Kind)
	}
	if m.Entrypoint.Module != "human-agent-im/internal/plugin/openclaw" {
		t.Errorf("entrypoint.module = %q", m.Entrypoint.Module)
	}

	// manifest.provides 是插件能力上限：必须覆盖代码里声明的全部能力常量。
	declared := map[string]bool{}
	for _, c := range m.Provides {
		declared[c.ID] = true
	}
	for _, capID := range []string{
		openclawplugin.CapWechatPersonalChannel,
		openclawplugin.CapAccountRegistry,
		openclawplugin.CapMCPRouteTool,
		openclawplugin.CapAgentGateway,
	} {
		if !declared[capID] {
			t.Errorf("manifest.provides missing code capability %q", capID)
		}
	}

	// 归属信息必须与 /v1/about/attribution 的第一条来源一致（许可证义务）。
	attr := openclawplugin.Attributions()[0]
	if m.Attribution.License != attr.License ||
		m.Attribution.Copyright != attr.Copyright ||
		m.Attribution.Upstream != attr.Upstream ||
		m.Attribution.NoticePath != attr.NoticePath ||
		m.Attribution.Derived != attr.Derived {
		t.Errorf("manifest.attribution drifts from plugin.Attributions()[0]:\n manifest=%+v\n plugin=%+v",
			m.Attribution, attr)
	}
}

// dsbot 最小 manifest 必须与 Go 插件代码一致。
func TestDSBotManifestMatchesPluginCode(t *testing.T) {
	items, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}
	var m *Manifest
	for _, it := range items {
		if it.Manifest.ID == dsbotplugin.PluginID {
			m = it.Manifest
		}
	}
	if m == nil {
		t.Fatal("bundled dsbot manifest missing")
	}
	if m.Version != dsbotplugin.PluginVersion {
		t.Errorf("manifest version %s != %s", m.Version, dsbotplugin.PluginVersion)
	}
	declared := map[string]bool{}
	for _, c := range m.Provides {
		declared[c.ID] = true
	}
	for _, capID := range []string{
		dsbotplugin.CapWebSearch,
		dsbotplugin.CapLongMessage,
		dsbotplugin.CapRosterSpec,
		dsbotplugin.CapBridge,
	} {
		if !declared[capID] {
			t.Errorf("manifest.provides missing code capability %q", capID)
		}
	}
	attr := dsbotplugin.Attribution()
	if m.Attribution.License != attr.License ||
		m.Attribution.Copyright != attr.Copyright ||
		m.Attribution.Upstream != attr.Upstream {
		t.Errorf("manifest.attribution drifts from dsbotplugin.Attribution(): %+v vs %+v", m.Attribution, attr)
	}
}

// 部署形态与测试形态两份 openclaw manifest 的 canonical hash 必须相同（防示例漂移）。
func TestDeployAndTestdataOpenClawSameHash(t *testing.T) {
	deploy := readRepoFile(t, "deploy/plugins/openclaw/manifest.json")
	testdata := readRepoFile(t, "internal/plugin/runtime/testdata/openclaw/manifest.json")

	deployCanonical, err := Canonicalize(deploy)
	if err != nil {
		t.Fatalf("canonical deploy: %v", err)
	}
	testCanonical, err := Canonicalize(testdata)
	if err != nil {
		t.Fatalf("canonical testdata: %v", err)
	}
	if !bytes.Equal(deployCanonical, testCanonical) {
		t.Fatalf("canonical mismatch between deploy and testdata copies")
	}
	_, deployHash, err := Parse(deploy)
	if err != nil {
		t.Fatalf("parse deploy: %v", err)
	}
	_, testHash, err := Parse(testdata)
	if err != nil {
		t.Fatalf("parse testdata: %v", err)
	}
	if deployHash != testHash {
		t.Fatalf("manifest_hash mismatch: deploy=%s testdata=%s", deployHash, testHash)
	}
}

// JSON Schema 的顶层字段必须与 Go 结构体 tag 完全一致（双向，防漂移）。
func TestSchemaCoversStructFields(t *testing.T) {
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		PatternProperties    map[string]json.RawMessage `json:"patternProperties"`
	}
	if err := json.Unmarshal(SchemaJSON(), &schema); err != nil {
		t.Fatalf("schema JSON invalid: %v", err)
	}
	if schema.AdditionalProperties {
		t.Fatal("schema must set additionalProperties=false (only x-* extensions allowed)")
	}
	if _, ok := schema.PatternProperties["^x-"]; !ok {
		t.Fatal("schema must allow x-* extension fields")
	}

	structFields := map[string]bool{}
	typ := reflect.TypeOf(Manifest{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		structFields[name] = true
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("struct field %q missing from JSON Schema properties", name)
		}
	}
	for name := range schema.Properties {
		if !structFields[name] {
			t.Errorf("JSON Schema property %q has no matching Go struct field", name)
		}
	}
	for _, req := range schema.Required {
		if _, ok := schema.Properties[req]; !ok {
			t.Errorf("required field %q missing from properties", req)
		}
		if !structFields[req] {
			t.Errorf("required field %q has no Go struct field", req)
		}
	}
	if len(schema.Required) < 10 {
		t.Errorf("schema.required too small (%d), required fields likely drifted", len(schema.Required))
	}
}

// PLUGIN_RUNTIME_V2=0 零行为变化的关键证据：**对外服务路径**（services/、internal/kernel、
// internal/api 等）不得 import 本包。D2 引入了运维 CLI `cmd/im2a-plugin`，它唯一被允许
// import 本包（人工操作入口，不参与网关请求路径）。D3 接线网关时需同步更新本测试并说明。
func TestRuntimePackageIsNotWired(t *testing.T) {
	root := repoRoot(t)
	selfDir := filepath.Join(root, "internal", "plugin", "runtime")
	allowedCLI := filepath.Join(root, "cmd", "im2a-plugin")
	needle := `"human-agent-im/internal/plugin/runtime"`
	scanned := 0
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "prod-v2327" {
				return filepath.SkipDir
			}
			if path == selfDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 运维 CLI 是唯一允许 import 本包的生产路径（不参与网关请求处理）。
		if strings.HasPrefix(path, allowedCLI) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if bytes.Contains(b, []byte(needle)) {
			rel, _ := filepath.Rel(root, path)
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if scanned < 100 {
		t.Fatalf("only scanned %d go files; repoRoot likely wrong (%s)", scanned, root)
	}
	if len(violations) > 0 {
		t.Fatalf("PLUGIN_RUNTIME_V2 default-off violated: production code imports internal/plugin/runtime: %v", violations)
	}
}
