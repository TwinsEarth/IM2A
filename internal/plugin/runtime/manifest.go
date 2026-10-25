// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Manifest schema 与能力契约版本。
const (
	APIVersion    = "im2a.plugin/v1"
	CapabilityAPI = "im2a.capability/v1"

	// MaxManifestBytes 单份 manifest 上限（防 DoS；正常 manifest < 32KiB）。
	MaxManifestBytes = 256 << 10
)

// Kind 是插件形态：K0 进程内 / K1 本机独立进程 / K2 跨机。
type Kind string

const (
	KindK0 Kind = "K0"
	KindK1 Kind = "K1"
	KindK2 Kind = "K2"
)

// Valid 报告 kind 是否合法。
func (k Kind) Valid() bool { return k == KindK0 || k == KindK1 || k == KindK2 }

// Installable 报告该形态能否从磁盘安装（K0 随内核编译，不可安装）。
func (k Kind) Installable() bool { return k == KindK1 || k == KindK2 }

// RequiresSignature 报告该形态是否必须有 manifest 签名。
func (k Kind) RequiresSignature() bool { return k == KindK1 || k == KindK2 }

// SecurityBoundary 是**硬裁决**：只有 K1/K2 才可能存在进程/主机级安全边界。
//
// K0 与内核同进程、静态链接，是受信代码；接口注入不是安全边界，os/unsafe 可绕过。
// 该函数不读取 manifest 内容、不可被配置覆盖；任何"K0 有隔离"的输出都是 bug。
func SecurityBoundary(k Kind) bool { return k == KindK1 || k == KindK2 }

// Publisher 发布者与签名 key。
type Publisher struct {
	ID    string `json:"id"`
	KeyID string `json:"keyId"`
}

// Entrypoint 启动入口；按 kind 使用不同字段。
type Entrypoint struct {
	Type   string            `json:"type"` // inprocess | exec | remote
	Module string            `json:"module,omitempty"`
	Path   string            `json:"path,omitempty"`
	Args   []string          `json:"args,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	URL    string            `json:"url,omitempty"`
}

// Capability 是插件对外提供的某项能力（manifest 声明的是**上限**，运行期还需自检）。
type Capability struct {
	ID        string   `json:"id"`
	Version   string   `json:"version"` // 能力接口版本，如 v1
	Modes     []string `json:"modes,omitempty"`
	Exclusive bool     `json:"exclusive,omitempty"`
	Priority  int      `json:"priority,omitempty"`
}

// CapabilityRef 是依赖引用。
type CapabilityRef struct {
	Capability string `json:"capability"`
	Version    string `json:"version,omitempty"`
}

// Permission 一条权限声明。K0 的该字段只是评审契约（见包注释）。
type Permission struct {
	Class string   `json:"class"`
	Scope []string `json:"scope"`
}

// Health 健康检查参数。
type Health struct {
	Type     string `json:"type,omitempty"` // exec | rpc | http
	Target   string `json:"target,omitempty"`
	Grace    string `json:"grace,omitempty"`
	Interval string `json:"interval,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
	Failures int    `json:"failures,omitempty"`
}

// Lifecycle 生命周期参数。
type Lifecycle struct {
	StartTimeout  string `json:"startTimeout,omitempty"`
	StopTimeout   string `json:"stopTimeout,omitempty"`
	Restart       string `json:"restart,omitempty"` // always | on-failure | never
	MaxRestarts   int    `json:"maxRestarts,omitempty"`
	RestartWindow string `json:"restartWindow,omitempty"`
}

// Resources 资源限额（K1 由 supervisor 物化）。
type Resources struct {
	MemoryMax string `json:"memoryMax,omitempty"`
	CPUMax    string `json:"cpuMax,omitempty"`
	PIDsMax   int    `json:"pidsMax,omitempty"`
}

// AutoRollback 自动回滚阈值。
type AutoRollback struct {
	HealthFailures int     `json:"healthFailures,omitempty"`
	ErrorRate      float64 `json:"errorRate,omitempty"`
	MinRequests    int     `json:"minRequests,omitempty"`
	Window         string  `json:"window,omitempty"`
	BakeTime       string  `json:"bakeTime,omitempty"`
}

// Rollback 回滚策略。
type Rollback struct {
	KeepVersions int          `json:"keepVersions,omitempty"`
	AutoRollback AutoRollback `json:"autoRollback,omitempty"`
}

// Attribution 上游归属（字段与 internal/plugin.Attribution 对齐，JSON 名一致）。
type Attribution struct {
	License      string   `json:"license"`
	Copyright    string   `json:"copyright"`
	Upstream     string   `json:"upstream"`
	UpstreamName string   `json:"upstreamName,omitempty"`
	NoticePath   string   `json:"noticePath,omitempty"`
	Derived      bool     `json:"derived,omitempty"`
	Usage        []string `json:"usage,omitempty"`
}

// Manifest 是插件清单的唯一真相。
type Manifest struct {
	APIVersion       string                     `json:"apiVersion"`
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Version          string                     `json:"version"`
	Kind             Kind                       `json:"kind"`
	API              string                     `json:"api"`
	MinKernelVersion string                     `json:"minKernelVersion,omitempty"`
	Publisher        Publisher                  `json:"publisher"`
	Entrypoint       Entrypoint                 `json:"entrypoint"`
	Targets          []string                   `json:"targets,omitempty"`
	Provides         []Capability               `json:"provides"`
	Requires         []CapabilityRef            `json:"requires,omitempty"`
	OptionalRequires []CapabilityRef            `json:"optional_requires,omitempty"`
	RequiresPlugins  []string                   `json:"requires_plugins,omitempty"`
	Permissions      []Permission               `json:"permissions"`
	ConfigSchema     string                     `json:"configSchema,omitempty"`
	Health           *Health                    `json:"health,omitempty"`
	Lifecycle        *Lifecycle                 `json:"lifecycle,omitempty"`
	Resources        *Resources                 `json:"resources,omitempty"`
	Rollback         *Rollback                  `json:"rollback,omitempty"`
	Attribution      Attribution                `json:"attribution"`
	Extensions       map[string]json.RawMessage `json:"-"`
}

// knownFields 是 v1 允许的顶层字段全集；其余非 x-* 字段一律拒绝（防静默忽略安全字段）。
var knownFields = map[string]bool{
	"apiVersion": true, "id": true, "name": true, "version": true, "kind": true,
	"api": true, "minKernelVersion": true, "publisher": true, "entrypoint": true,
	"targets": true, "provides": true, "requires": true, "optional_requires": true,
	"requires_plugins": true, "permissions": true, "configSchema": true,
	"health": true, "lifecycle": true, "resources": true, "rollback": true,
	"attribution": true,
}

// Issue 是一条校验/加载问题（具名 code，便于测试与运维检索）。
type Issue struct {
	Code     string `json:"code"`
	PluginID string `json:"pluginId,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

// 校验/加载错误码（稳定字符串，日志与测试依赖）。
const (
	CodeSchemaMissing         = "E_SCHEMA_MISSING"
	CodeSchemaInvalid         = "E_SCHEMA_INVALID"
	CodeUnknownField          = "E_UNKNOWN_FIELD"
	CodeInvalidID             = "E_INVALID_ID"
	CodeInvalidVersion        = "E_INVALID_VERSION"
	CodeInvalidKind           = "E_INVALID_KIND"
	CodeInvalidAPI            = "E_INVALID_API"
	CodeInvalidEntrypoint     = "E_INVALID_ENTRYPOINT"
	CodeInvalidProvides       = "E_INVALID_PROVIDES"
	CodeDuplicateCapability   = "E_DUP_CAPABILITY"
	CodeInvalidRequires       = "E_INVALID_REQUIRES"
	CodeInvalidPermission     = "E_INVALID_PERMISSION"
	CodeInvalidAttribution    = "E_INVALID_ATTRIBUTION"
	CodeInvalidHealth         = "E_INVALID_HEALTH"
	CodeInvalidResources      = "E_INVALID_RESOURCES"
	CodeInvalidRollback       = "E_INVALID_ROLLBACK"
	CodeInvalidConfigSchema   = "E_INVALID_CONFIG_SCHEMA"
	CodeTooLarge              = "E_MANIFEST_TOO_LARGE"
	CodeK0OnDisk              = "E_K0_ON_DISK"
	CodeUnsupportedPlatform   = "E_UNSUPPORTED_PLATFORM"
	CodeKernelTooOld          = "E_KERNEL_TOO_OLD"
	CodeDependencyMissing     = "E_DEP_MISSING"
	CodeDependencyCycle       = "E_DEP_CYCLE"
	CodeDuplicatePlugin       = "E_DUP_PLUGIN"
	CodeInvalidDirLayout      = "E_INVALID_DIR_LAYOUT"
	CodeMultipleActiveVer     = "E_MULTIPLE_ACTIVE_VERSION"
	CodeIO                    = "E_IO"
	CodeSignatureRequired     = "E_SIG_REQUIRED"
	CodeSignatureMissing      = "E_SIG_MISSING"
	CodeSignatureMalformed    = "E_SIG_MALFORMED"
	CodeUnknownPublisherKey   = "E_SIG_UNKNOWN_KEY"
	CodeBadSignature          = "E_SIG_BAD"
	CodeTrustStoreUnavailable = "E_SIG_TRUST_STORE"
)

// ValidationError 聚合全部校验问题（不 fail-fast，便于一次修完）。
type ValidationError struct {
	ManifestID string  `json:"manifestId,omitempty"`
	Issues     []Issue `json:"issues"`
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("plugin runtime: manifest invalid")
	if e.ManifestID != "" {
		b.WriteString(" (" + e.ManifestID + ")")
	}
	for i, is := range e.Issues {
		if i >= 8 {
			b.WriteString(fmt.Sprintf("; ... and %d more", len(e.Issues)-8))
			break
		}
		b.WriteString(fmt.Sprintf("; %s %s: %s", is.Code, is.Path, is.Message))
	}
	return b.String()
}

// Unwrap 让 errors.Is(err, ErrInvalidManifest) 成立。
func (e *ValidationError) Unwrap() error { return ErrInvalidManifest }

// ErrInvalidManifest 是 manifest 校验失败的错误基类。
var ErrInvalidManifest = errors.New("plugin runtime: invalid manifest")

// Parse 解析并校验 manifest 字节串，返回有效 Manifest 与 manifest_hash。
// 任何 schema/语义问题返回 *ValidationError（含全部 issues）。
func Parse(raw []byte) (*Manifest, string, error) {
	if len(raw) == 0 {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeSchemaMissing, Path: "$", Message: "empty manifest"}}}
	}
	if len(raw) > MaxManifestBytes {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeTooLarge, Path: "$", Message: fmt.Sprintf("manifest %d bytes > %d", len(raw), MaxManifestBytes)}}}
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeSchemaInvalid, Path: "$", Message: err.Error()}}}
	}
	hash := HashCanonical(canonical)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &fields); err != nil {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeSchemaInvalid, Path: "$", Message: err.Error()}}}
	}
	known := make(map[string]json.RawMessage, len(fields))
	ext := map[string]json.RawMessage{}
	for k, v := range fields {
		switch {
		case knownFields[k]:
			known[k] = v
		case strings.HasPrefix(k, "x-"):
			ext[k] = v
		default:
			return nil, "", &ValidationError{Issues: []Issue{{Code: CodeUnknownField, Path: "$." + k, Message: "unknown field (use x-* for extensions)"}}}
		}
	}
	// 只把已知字段喂给 typed 解码（Extensions 不参与 JSON 解码）。
	filtered, err := json.Marshal(known) // map => keys 排序，确定性
	if err != nil {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeSchemaInvalid, Path: "$", Message: err.Error()}}}
	}
	dec := json.NewDecoder(bytes.NewReader(filtered))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, "", &ValidationError{Issues: []Issue{{Code: CodeSchemaInvalid, Path: "$", Message: fmt.Sprintf("decode: %v", err)}}}
	}
	if len(ext) > 0 {
		m.Extensions = ext
	}
	if issues := Validate(&m); len(issues) > 0 {
		return nil, hash, &ValidationError{ManifestID: m.ID, Issues: issues}
	}
	return &m, hash, nil
}

var (
	reID          = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,63}$`)
	reVersion     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	reKernelVer   = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)
	reCapability  = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+$`)
	reCapVersion  = regexp.MustCompile(`^v[0-9]+$`)
	reTarget      = regexp.MustCompile(`^(linux|darwin|windows)/(amd64|arm64)$`)
	reDuration    = regexp.MustCompile(`^[0-9]+(ms|s|m|h)$`)
	reMemLimit    = regexp.MustCompile(`^[0-9]+(MiB|GiB)$`)
	reKeyID       = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)
	permissionSet = map[string]bool{
		"storage.read": true, "storage.write": true, "net.outbound": true,
		"fs.read": true, "fs.write": true, "exec": true, "secrets": true,
		"eventbus.publish": true, "eventbus.subscribe": true,
		"capability.call": true, "http.admin": true,
	}
)

// Validate 对已解析的 Manifest 做语义校验，返回全部问题。
func Validate(m *Manifest) []Issue {
	if m == nil {
		return []Issue{{Code: CodeSchemaMissing, Path: "$", Message: "nil manifest"}}
	}
	var out []Issue
	add := func(code, path, msg string) { out = append(out, Issue{Code: code, Path: path, Message: msg}) }
	require := func(cond bool, code, path, msg string) {
		if !cond {
			add(code, path, msg)
		}
	}
	// 必填字段缺失（JSON Schema `required` 的运行时等价物）：先给 E_SCHEMA_MISSING，
	// 再做各字段的格式校验（缺失时格式校验会再报一条，便于定位）。
	requiredStr := func(val, path string) {
		if strings.TrimSpace(val) == "" {
			add(CodeSchemaMissing, path, "required field is missing or empty")
		}
	}
	requiredStr(m.APIVersion, "$.apiVersion")
	requiredStr(m.ID, "$.id")
	requiredStr(m.Name, "$.name")
	requiredStr(m.Version, "$.version")
	requiredStr(string(m.Kind), "$.kind")
	requiredStr(m.API, "$.api")
	requiredStr(m.Publisher.ID, "$.publisher.id")
	requiredStr(m.Publisher.KeyID, "$.publisher.keyId")
	requiredStr(m.Entrypoint.Type, "$.entrypoint.type")
	requiredStr(m.Attribution.License, "$.attribution.license")
	requiredStr(m.Attribution.Copyright, "$.attribution.copyright")
	requiredStr(m.Attribution.Upstream, "$.attribution.upstream")
	if m.Provides == nil {
		add(CodeSchemaMissing, "$.provides", "required array is missing")
	}
	if m.Permissions == nil {
		add(CodeSchemaMissing, "$.permissions", "required array is missing (use [] for zero permissions)")
	}

	require(m.APIVersion == APIVersion, CodeInvalidAPI, "$.apiVersion", fmt.Sprintf("must be %q", APIVersion))
	require(reID.MatchString(m.ID), CodeInvalidID, "$.id", "must match ^[a-z][a-z0-9.-]{1,63}$")
	require(len(m.Name) > 0 && len([]rune(m.Name)) <= 64, CodeSchemaInvalid, "$.name", "required, 1..64 runes")
	require(reVersion.MatchString(m.Version), CodeInvalidVersion, "$.version", "must be SemVer X.Y.Z[-pre]")
	require(m.Kind.Valid(), CodeInvalidKind, "$.kind", "must be K0|K1|K2")
	require(m.API == CapabilityAPI, CodeInvalidAPI, "$.api", fmt.Sprintf("must be %q", CapabilityAPI))
	if m.MinKernelVersion != "" {
		require(reKernelVer.MatchString(m.MinKernelVersion), CodeInvalidVersion, "$.minKernelVersion", "must be vX.Y.Z")
	}
	require(reID.MatchString(m.Publisher.ID), CodeInvalidID, "$.publisher.id", "invalid publisher id")
	require(reKeyID.MatchString(m.Publisher.KeyID), CodeInvalidAPI, "$.publisher.keyId", "must match ^[A-Za-z0-9._-]{8,64}$")

	// entrypoint 按 kind 校验。
	require(m.Entrypoint.Type != "", CodeInvalidEntrypoint, "$.entrypoint.type", "required")
	switch m.Kind {
	case KindK0:
		require(m.Entrypoint.Type == "inprocess", CodeInvalidEntrypoint, "$.entrypoint.type", "K0 requires inprocess")
		require(m.Entrypoint.Module != "", CodeInvalidEntrypoint, "$.entrypoint.module", "K0 requires module")
	case KindK1:
		require(m.Entrypoint.Type == "exec", CodeInvalidEntrypoint, "$.entrypoint.type", "K1 requires exec")
		require(safeRelPath(m.Entrypoint.Path), CodeInvalidEntrypoint, "$.entrypoint.path", "K1 requires safe relative path")
	case KindK2:
		require(m.Entrypoint.Type == "remote", CodeInvalidEntrypoint, "$.entrypoint.type", "K2 requires remote")
		require(strings.HasPrefix(m.Entrypoint.URL, "https://") || strings.HasPrefix(m.Entrypoint.URL, "wss://"), CodeInvalidEntrypoint, "$.entrypoint.url", "K2 requires https:// or wss:// URL")
	}
	for i, t := range m.Targets {
		require(reTarget.MatchString(t), CodeSchemaInvalid, fmt.Sprintf("$.targets[%d]", i), "must be GOOS/GOARCH")
	}

	// provides。
	require(len(m.Provides) > 0, CodeInvalidProvides, "$.provides", "at least one capability required")
	seenCap := map[string]bool{}
	for i := range m.Provides {
		c := &m.Provides[i]
		require(reCapability.MatchString(c.ID), CodeInvalidProvides, fmt.Sprintf("$.provides[%d].id", i), "invalid capability id")
		require(reCapVersion.MatchString(c.Version), CodeInvalidProvides, fmt.Sprintf("$.provides[%d].version", i), "must be v<digits>")
		require(c.Priority >= -1000 && c.Priority <= 1000, CodeInvalidProvides, fmt.Sprintf("$.provides[%d].priority", i), "must be -1000..1000")
		if seenCap[c.ID] {
			add(CodeDuplicateCapability, fmt.Sprintf("$.provides[%d]", i), "duplicate capability "+c.ID)
		}
		seenCap[c.ID] = true
	}

	// requires / optional_requires。
	checkRefs := func(refs []CapabilityRef, path string) {
		seen := map[string]bool{}
		for i := range refs {
			r := &refs[i]
			require(reCapability.MatchString(r.Capability), CodeInvalidRequires, fmt.Sprintf("%s[%d].capability", path, i), "invalid capability id")
			if r.Version != "" {
				require(reCapVersion.MatchString(r.Version), CodeInvalidRequires, fmt.Sprintf("%s[%d].version", path, i), "must be v<digits>")
			}
			key := r.Capability + "@" + r.Version
			if seen[key] {
				add(CodeInvalidRequires, fmt.Sprintf("%s[%d]", path, i), "duplicate requirement "+key)
			}
			seen[key] = true
		}
	}
	checkRefs(m.Requires, "$.requires")
	checkRefs(m.OptionalRequires, "$.optional_requires")

	seenPlugin := map[string]bool{}
	for i, id := range m.RequiresPlugins {
		require(reID.MatchString(id), CodeInvalidRequires, fmt.Sprintf("$.requires_plugins[%d]", i), "invalid plugin id")
		require(id != m.ID, CodeInvalidRequires, fmt.Sprintf("$.requires_plugins[%d]", i), "plugin cannot require itself")
		if seenPlugin[id] {
			add(CodeInvalidRequires, fmt.Sprintf("$.requires_plugins[%d]", i), "duplicate plugin requirement "+id)
		}
		seenPlugin[id] = true
	}

	// permissions（空数组合法 = 零权限；每项 scope 至少 1 条）。
	seenPerm := map[string]bool{}
	for i := range m.Permissions {
		p := &m.Permissions[i]
		require(permissionSet[p.Class], CodeInvalidPermission, fmt.Sprintf("$.permissions[%d].class", i), "unknown permission class "+p.Class)
		require(len(p.Scope) > 0, CodeInvalidPermission, fmt.Sprintf("$.permissions[%d].scope", i), "scope must not be empty")
		for j, sc := range p.Scope {
			require(strings.TrimSpace(sc) != "" && strings.TrimSpace(sc) == sc, CodeInvalidPermission, fmt.Sprintf("$.permissions[%d].scope[%d]", i, j), "scope entry must be non-empty and trimmed")
		}
		key := p.Class + "\x00" + strings.Join(p.Scope, "\x00")
		if seenPerm[key] {
			add(CodeInvalidPermission, fmt.Sprintf("$.permissions[%d]", i), "duplicate permission")
		}
		seenPerm[key] = true
	}

	// configSchema。
	if m.ConfigSchema != "" {
		require(safeRelPath(m.ConfigSchema), CodeInvalidConfigSchema, "$.configSchema", "must be safe relative path")
	}

	// health / lifecycle / resources / rollback（可省略；K1/K2 建议提供）。
	if m.Health != nil {
		if m.Health.Type != "" {
			require(m.Health.Type == "exec" || m.Health.Type == "rpc" || m.Health.Type == "http", CodeInvalidHealth, "$.health.type", "exec|rpc|http")
		}
		for _, f := range []struct{ name, v string }{{"grace", m.Health.Grace}, {"interval", m.Health.Interval}, {"timeout", m.Health.Timeout}} {
			if f.v != "" {
				require(reDuration.MatchString(f.v), CodeInvalidHealth, "$.health."+f.name, "duration like 10s")
			}
		}
		if m.Health.Failures < 0 || m.Health.Failures > 10 {
			add(CodeInvalidHealth, "$.health.failures", "must be 0..10")
		}
	}
	if m.Lifecycle != nil {
		if m.Lifecycle.Restart != "" {
			require(m.Lifecycle.Restart == "always" || m.Lifecycle.Restart == "on-failure" || m.Lifecycle.Restart == "never", CodeSchemaInvalid, "$.lifecycle.restart", "always|on-failure|never")
		}
		for _, f := range []struct{ name, v string }{{"startTimeout", m.Lifecycle.StartTimeout}, {"stopTimeout", m.Lifecycle.StopTimeout}, {"restartWindow", m.Lifecycle.RestartWindow}} {
			if f.v != "" {
				require(reDuration.MatchString(f.v), CodeSchemaInvalid, "$.lifecycle."+f.name, "duration like 30s")
			}
		}
	}
	if m.Resources != nil {
		if m.Resources.MemoryMax != "" {
			require(reMemLimit.MatchString(m.Resources.MemoryMax), CodeInvalidResources, "$.resources.memoryMax", "like 256MiB")
		}
		if m.Resources.PIDsMax < 0 {
			add(CodeInvalidResources, "$.resources.pidsMax", "must be >= 0")
		}
	}
	if m.Rollback != nil {
		if m.Rollback.KeepVersions != 0 {
			require(m.Rollback.KeepVersions >= 1 && m.Rollback.KeepVersions <= 5, CodeInvalidRollback, "$.rollback.keepVersions", "must be 1..5")
		}
		ar := m.Rollback.AutoRollback
		if ar.ErrorRate != 0 {
			require(ar.ErrorRate >= 0 && ar.ErrorRate <= 1, CodeInvalidRollback, "$.rollback.autoRollback.errorRate", "must be 0..1")
		}
		for _, f := range []struct{ name, v string }{{"window", ar.Window}, {"bakeTime", ar.BakeTime}} {
			if f.v != "" {
				require(reDuration.MatchString(f.v), CodeInvalidRollback, "$.rollback.autoRollback."+f.name, "duration like 60s")
			}
		}
	}

	// attribution（第三方归属是硬义务，不允许空）。
	require(strings.TrimSpace(m.Attribution.License) != "", CodeInvalidAttribution, "$.attribution.license", "required")
	require(strings.TrimSpace(m.Attribution.Copyright) != "", CodeInvalidAttribution, "$.attribution.copyright", "required")
	require(strings.TrimSpace(m.Attribution.Upstream) != "", CodeInvalidAttribution, "$.attribution.upstream", "required")

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// safeRelPath 判断相对路径安全：非空、非绝对、不含 ".." 或反斜杠。
func safeRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	return true
}
