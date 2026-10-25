// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
)

// 插件状态（loader 视角；D2 接 Host 时映射到 plugin.State*）。
const (
	StateOK       = "ok"
	StateDegraded = "degraded"
	StateBlocked  = "blocked"
)

// 加载来源。
const (
	SourceBundled   = "bundled"
	SourceInstalled = "installed"
)

// 非致命告警码（进 Result.Issues，不中止加载）。
const (
	WarnOptionalDepMissing = "W_DEP_OPTIONAL_MISSING"
	WarnMultipleVersions   = "W_MULTIPLE_VERSIONS"
)

// Loaded 是一份已加载 manifest 的结果（含签名/哈希/状态）。
type Loaded struct {
	Manifest     *Manifest
	ManifestHash string
	Signature    SignatureStatus
	Source       string
	Path         string
	State        string
	Detail       string
}

// SecurityBoundary 返回该插件是否存在安全边界（K0 恒 false，见 SecurityBoundary 注释）。
func (l *Loaded) SecurityBoundary() bool {
	if l == nil || l.Manifest == nil {
		return false
	}
	return SecurityBoundary(l.Manifest.Kind)
}

// LoadError 是加载期具名错误（Code 见 E_* 常量；Err 保留底层错误供 errors.Is）。
type LoadError struct {
	Code    string
	Path    string
	Message string
	Err     error
}

func (e *LoadError) Error() string {
	if e == nil {
		return ""
	}
	msg := "plugin runtime: " + e.Code
	if e.Path != "" {
		msg += " (" + e.Path + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *LoadError) Unwrap() error { return e.Err }

// Options 是 loader 配置（零值可用）。
type Options struct {
	// Trust 是 K1/K2 验签的受信公钥集合；K1/K2 缺失 → ErrTrustStoreUnavailable。
	Trust *TrustStore
	// Platform 缺省 runtime.GOOS/GOARCH；manifest.targets 非空且不含则拒绝。
	Platform string
	// KernelVersion 用于 minKernelVersion 校验（空 = 跳过）。
	KernelVersion string
	// KernelCapabilities 是内核自身提供的能力（不需要插件 provides），
	// 缺省 DefaultKernelCapabilities()。
	KernelCapabilities []string
	// StrictRequired=true 时，缺失必需依赖/必需插件直接返回错误；默认只标记 blocked。
	StrictRequired bool
}

// platform 返回生效平台。
func (o Options) platform() string {
	if strings.TrimSpace(o.Platform) != "" {
		return o.Platform
	}
	return defaultPlatform()
}

func (o Options) kernelCaps() map[string]bool {
	caps := o.KernelCapabilities
	if caps == nil {
		caps = DefaultKernelCapabilities()
	}
	out := make(map[string]bool, len(caps))
	for _, c := range caps {
		out[c] = true
	}
	return out
}

// DefaultKernelCapabilities 是内核自身提供的能力清单（插件可在 requires 中引用，
// 不会因为"没有插件提供"被判缺失）。D2 接线时以内核真实注册表为准，此处只是 loader 的
// 静态默认值，保持可预测。
func DefaultKernelCapabilities() []string {
	return []string{
		"kernel.eventbus",
		"kernel.audit",
		"kernel.clock",
		"kernel.storage",
		"kernel.secrets",
	}
}

//go:embed testdata/openclaw/manifest.json testdata/dsbot/manifest.json
var bundledFS embed.FS

// bundledFiles 是编译期内建插件（K0）manifest 清单：随内核构建、无需签名。
// D1 后半接线时它们会改为各插件包内的 //go:embed（本包不 import 具体插件，避免环）。
var bundledFiles = []string{
	"testdata/openclaw/manifest.json",
	"testdata/dsbot/manifest.json",
}

// LoadBundled 加载编译期内建 K0 manifest：校验 schema/语义，签名状态=not_required
// （K0 随内核构建，供应链=内核构建链，不要求 detached 签名）。
func LoadBundled() ([]*Loaded, error) {
	out := make([]*Loaded, 0, len(bundledFiles))
	for _, name := range bundledFiles {
		raw, err := bundledFS.ReadFile(name)
		if err != nil {
			return nil, &LoadError{Code: CodeIO, Path: name, Message: "read embedded manifest", Err: err}
		}
		m, hash, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("bundled %s: %w", name, err)
		}
		if m.Kind != KindK0 {
			return nil, &LoadError{Code: CodeInvalidKind, Path: name, Message: fmt.Sprintf("bundled plugin must be K0, got %s", m.Kind)}
		}
		out = append(out, &Loaded{
			Manifest: m, ManifestHash: hash, Signature: SigNotRequired,
			Source: SourceBundled, Path: name, State: StateOK,
		})
	}
	return out, nil
}

// LoadInstalled 扫描 `<root>/<pluginID>/<version>/manifest.json`（+ manifest.sig），
// 完成 schema/签名/平台/内核版本校验，并做同 id 多版本选择（取最高版本，其余告警）。
// 返回结果尚未做依赖拓扑（由 BuildResult 完成），以便调用方分别测试。
func LoadInstalled(root string, opts Options) ([]*Loaded, []Issue, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, &LoadError{Code: CodeIO, Path: root, Message: "read plugin root", Err: err}
	}
	byID := map[string][]*Loaded{}
	var issues []Issue
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		pluginDir := filepath.Join(root, e.Name())
		verEntries, err := os.ReadDir(pluginDir)
		if err != nil {
			return nil, nil, &LoadError{Code: CodeIO, Path: pluginDir, Err: err}
		}
		for _, ve := range verEntries {
			if !ve.IsDir() {
				continue
			}
			manifestPath := filepath.Join(pluginDir, ve.Name(), "manifest.json")
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, nil, &LoadError{Code: CodeIO, Path: manifestPath, Err: err}
			}
			m, hash, err := Parse(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", manifestPath, err)
			}
			if !m.Kind.Installable() {
				return nil, nil, &LoadError{Code: CodeK0OnDisk, Path: manifestPath,
					Message: "K0 plugins are compiled into the kernel and cannot be installed from disk"}
			}
			if opts.Trust == nil || opts.Trust.Len() == 0 {
				return nil, nil, &LoadError{Code: CodeTrustStoreUnavailable, Path: manifestPath,
					Message: "K1/K2 manifest requires a trust store", Err: ErrTrustStoreUnavailable}
			}
			sigPath := filepath.Join(pluginDir, ve.Name(), "manifest.sig")
			sig, err := os.ReadFile(sigPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, nil, &LoadError{Code: CodeSignatureMissing, Path: manifestPath, Err: ErrSignatureMissing}
				}
				return nil, nil, &LoadError{Code: CodeIO, Path: sigPath, Err: err}
			}
			if err := Verify(raw, sig, opts.Trust); err != nil {
				code := CodeBadSignature
				switch SignatureState(err) {
				case SigMissing:
					code = CodeSignatureMissing
				case SigMalformed:
					code = CodeSignatureMalformed
				case SigUnknownKey:
					code = CodeUnknownPublisherKey
				}
				return nil, nil, &LoadError{Code: code, Path: manifestPath, Err: err}
			}
			if len(m.Targets) > 0 && !containsString(m.Targets, opts.platform()) {
				return nil, nil, &LoadError{Code: CodeUnsupportedPlatform, Path: manifestPath,
					Message: fmt.Sprintf("targets %v do not include %s", m.Targets, opts.platform())}
			}
			if m.MinKernelVersion != "" && opts.KernelVersion != "" && compareSemver(normalizeVer(opts.KernelVersion), normalizeVer(m.MinKernelVersion)) < 0 {
				return nil, nil, &LoadError{Code: CodeKernelTooOld, Path: manifestPath,
					Message: fmt.Sprintf("kernel %s < minKernelVersion %s", opts.KernelVersion, m.MinKernelVersion)}
			}
			byID[m.ID] = append(byID[m.ID], &Loaded{
				Manifest: m, ManifestHash: hash, Signature: SigValid,
				Source: SourceInstalled, Path: manifestPath, State: StateOK,
			})
		}
	}
	out := make([]*Loaded, 0, len(byID))
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		versions := byID[id]
		sort.SliceStable(versions, func(i, j int) bool {
			return compareSemver(versions[i].Manifest.Version, versions[j].Manifest.Version) > 0
		})
		if len(versions) > 1 {
			issues = append(issues, Issue{Code: WarnMultipleVersions, Path: filepath.Join(root, id),
				Message: fmt.Sprintf("%d versions installed, using highest %s", len(versions), versions[0].Manifest.Version)})
		}
		out = append(out, versions[0])
	}
	return out, issues, nil
}

// Result 是 loader 的只读结果：已加载插件 + 依赖拓扑后的启动顺序 + 告警。
// D2 通过 NewLoader(...).Load() 取它，再调用 Host.StartOrdered(StartupOrder())。
type Result struct {
	Items  []*Loaded
	Order  []string
	Issues []Issue
}

// Get 按 id 查询（只读）。
func (r *Result) Get(id string) (*Loaded, bool) {
	if r == nil {
		return nil, false
	}
	for _, it := range r.Items {
		if it.Manifest != nil && it.Manifest.ID == id {
			return it, true
		}
	}
	return nil, false
}

// StartupOrder 返回拓扑排序后的启动顺序（不含 blocked）。
func (r *Result) StartupOrder() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.Order...)
}

// BuildResult 对已加载清单做依赖图构建 + 拓扑排序 + 状态标记。
//
// 规则：
//   - requires_plugins 与"只有唯一 provider 的 requires 能力"构成硬边；
//   - 缺失必需能力/必需插件 → blocked（StrictRequired 时直接返回 LoadError）；
//   - optional_requires 缺失 → degraded（不阻塞启动）；
//   - 有环 → LoadError E_DEP_CYCLE（始终致命）；
//   - 同一层（就绪集合）按 plugin id 字典序，保证确定性；blocked 不进入顺序。
func BuildResult(items []*Loaded, opts Options) (*Result, error) {
	res := &Result{Items: append([]*Loaded(nil), items...)}
	// 幂等：同一批 Loaded 可能被不同 Options 多次构建（如非严格/严格各一次），
	// 先复位状态，避免上一次调用留下的 blocked/degraded 影响本次判定。
	for _, it := range res.Items {
		it.State = StateOK
		it.Detail = ""
	}
	sort.SliceStable(res.Items, func(i, j int) bool { return res.Items[i].Manifest.ID < res.Items[j].Manifest.ID })
	byID := map[string]*Loaded{}
	provider := map[string][]string{} // capability → plugin ids
	for _, it := range res.Items {
		id := it.Manifest.ID
		if _, dup := byID[id]; dup {
			return nil, &LoadError{Code: CodeDuplicatePlugin, Path: id, Message: "duplicate plugin id"}
		}
		byID[id] = it
		for _, c := range it.Manifest.Provides {
			provider[c.ID] = append(provider[c.ID], id)
		}
	}
	kernelCaps := opts.kernelCaps()

	edges := make(map[string]map[string]bool, len(res.Items)) // plugin → deps
	indeg := make(map[string]int, len(res.Items))
	for _, it := range res.Items {
		edges[it.Manifest.ID] = map[string]bool{}
		indeg[it.Manifest.ID] = 0
	}

	block := func(it *Loaded, code, msg string) error {
		it.State = StateBlocked
		it.Detail = msg
		res.Issues = append(res.Issues, Issue{Code: code, PluginID: it.Manifest.ID, Path: "$", Message: msg})
		if opts.StrictRequired {
			return &LoadError{Code: code, Path: it.Manifest.ID, Message: msg}
		}
		return nil
	}

	for _, it := range res.Items {
		id := it.Manifest.ID
		// 显式插件依赖。
		for _, dep := range it.Manifest.RequiresPlugins {
			if _, ok := byID[dep]; !ok {
				if err := block(it, CodeDependencyMissing, "required plugin not installed: "+dep); err != nil {
					return nil, err
				}
				break
			}
			edges[id][dep] = true
		}
		if it.State == StateBlocked {
			continue
		}
		// 能力依赖：唯一 provider → 硬边；多 provider → 运行期路由，无硬边。
		for _, req := range it.Manifest.Requires {
			if kernelCaps[req.Capability] {
				continue
			}
			provs := provider[req.Capability]
			if len(provs) == 0 {
				if err := block(it, CodeDependencyMissing, "required capability not provided: "+req.Capability); err != nil {
					return nil, err
				}
				break
			}
			if len(provs) == 1 && provs[0] != id {
				edges[id][provs[0]] = true
			}
		}
		if it.State == StateBlocked {
			continue
		}
		// 可选依赖：只影响状态。
		for _, req := range it.Manifest.OptionalRequires {
			if kernelCaps[req.Capability] || len(provider[req.Capability]) > 0 {
				continue
			}
			if it.State == StateOK {
				it.State = StateDegraded
			}
			if it.Detail != "" {
				it.Detail += "; "
			}
			it.Detail += "optional capability missing: " + req.Capability
			res.Issues = append(res.Issues, Issue{Code: WarnOptionalDepMissing, PluginID: id, Path: "$", Message: "optional capability missing: " + req.Capability})
		}
	}

	// 依赖传播：被 block 的依赖会把依赖方一起 block（避免"依赖不启动、消费者启动"）。
	for changed := true; changed; {
		changed = false
		for _, it := range res.Items {
			if it.State == StateBlocked {
				continue
			}
			for dep := range edges[it.Manifest.ID] {
				if byID[dep].State == StateBlocked {
					it.State = StateBlocked
					it.Detail = "required dependency blocked: " + dep
					res.Issues = append(res.Issues, Issue{Code: CodeDependencyMissing, PluginID: it.Manifest.ID, Path: "$", Message: it.Detail})
					changed = true
					break
				}
			}
		}
	}

	// Kahn 拓扑：只在 startable 子图上做；同层按 id 字典序，保证确定性输出。
	startable := make(map[string]bool, len(res.Items))
	for _, it := range res.Items {
		if it.State != StateBlocked {
			startable[it.Manifest.ID] = true
		}
	}
	for id := range indeg {
		indeg[id] = 0
	}
	dependents := make(map[string][]string, len(startable))
	for id := range startable {
		for dep := range edges[id] {
			if !startable[dep] {
				continue
			}
			indeg[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}
	ready := make([]string, 0, len(startable))
	for id := range startable {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		res.Order = append(res.Order, cur)
		for _, dependent := range dependents[cur] {
			indeg[dependent]--
			if indeg[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(res.Order) != len(startable) {
		skip := map[string]bool{}
		for _, id := range res.Order {
			skip[id] = true
		}
		for _, it := range res.Items {
			if it.State == StateBlocked {
				skip[it.Manifest.ID] = true
			}
		}
		return nil, &LoadError{Code: CodeDependencyCycle, Path: strings.Join(findCycle(edges, skip), " -> "),
			Message: "dependency cycle detected"}
	}
	return res, nil
}

// findCycle 在剩余未排序节点中找一个环，返回可读路径（最多 8 跳）。
func findCycle(edges map[string]map[string]bool, ordered map[string]bool) []string {
	var nodes []string
	for id := range edges {
		if !ordered[id] {
			nodes = append(nodes, id)
		}
	}
	sort.Strings(nodes)
	state := map[string]int{} // 0 未访问 1 栈中 2 完成
	var stack []string
	var cycle []string
	var dfs func(string) bool
	dfs = func(id string) bool {
		state[id] = 1
		stack = append(stack, id)
		deps := make([]string, 0, len(edges[id]))
		for dep := range edges[id] {
			if ordered[dep] {
				continue
			}
			deps = append(deps, dep)
		}
		sort.Strings(deps)
		for _, dep := range deps {
			switch state[dep] {
			case 1:
				idx := 0
				for i, s := range stack {
					if s == dep {
						idx = i
						break
					}
				}
				cycle = append(append([]string(nil), stack[idx:]...), dep)
				return true
			case 0:
				if dfs(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
		return false
	}
	for _, n := range nodes {
		if state[n] == 0 && dfs(n) {
			return cycle
		}
	}
	if len(nodes) > 0 {
		return append(nodes[:1], nodes[0])
	}
	return nil
}

// Loader 是只读 loader 接口（D2 通过它拿到 Result；不接管 Host 启动）。
type Loader struct {
	Root        string
	Opts        Options
	SkipBundled bool // true = 不加载编译期内建 K0（只扫目录）
}

// NewLoader 构造 loader；默认包含内建 K0 manifest。
func NewLoader(root string, opts Options) *Loader {
	return &Loader{Root: root, Opts: opts}
}

// Load 执行：内建清单 + 目录扫描 → 依赖拓扑 → Result。
func (l *Loader) Load() (*Result, error) {
	var items []*Loaded
	var issues []Issue
	if !l.SkipBundled {
		bundled, err := LoadBundled()
		if err != nil {
			return nil, err
		}
		items = append(items, bundled...)
	}
	if strings.TrimSpace(l.Root) != "" {
		installed, fileIssues, err := LoadInstalled(l.Root, l.Opts)
		if err != nil {
			return nil, err
		}
		items = append(items, installed...)
		issues = append(issues, fileIssues...)
	}
	res, err := BuildResult(items, l.Opts)
	if err != nil {
		return nil, err
	}
	res.Issues = append(issues, res.Issues...)
	return res, nil
}

// listItem 是 `im2a-plugin list --json` 的单条输出（含 security_boundary/manifest_hash/签名状态）。
type listItem struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Version          string       `json:"version"`
	Kind             Kind         `json:"kind"`
	API              string       `json:"api"`
	ManifestHash     string       `json:"manifest_hash"`
	Signature        string       `json:"signature"`
	SecurityBoundary bool         `json:"security_boundary"`
	State            string       `json:"state"`
	Detail           string       `json:"detail,omitempty"`
	Source           string       `json:"source"`
	Path             string       `json:"path,omitempty"`
	Capabilities     []Capability `json:"capabilities"`
	Attribution      Attribution  `json:"attribution"`
}

// JSON 输出 loader 结果（`im2a-plugin list --json` 的等价实现）。
func (r *Result) JSON() ([]byte, error) {
	out := struct {
		Plugins      []listItem `json:"plugins"`
		StartupOrder []string   `json:"startup_order"`
		Issues       []Issue    `json:"issues,omitempty"`
	}{StartupOrder: []string{}, Plugins: []listItem{}}
	if r != nil {
		out.StartupOrder = append(out.StartupOrder, r.Order...)
		for _, it := range r.Items {
			out.Plugins = append(out.Plugins, listItem{
				ID: it.Manifest.ID, Name: it.Manifest.Name, Version: it.Manifest.Version,
				Kind: it.Manifest.Kind, API: it.Manifest.API,
				ManifestHash: it.ManifestHash, Signature: string(it.Signature),
				SecurityBoundary: it.SecurityBoundary(),
				State:            it.State, Detail: it.Detail,
				Source: it.Source, Path: it.Path,
				Capabilities: it.Manifest.Provides, Attribution: it.Manifest.Attribution,
			})
		}
		if r.Issues != nil {
			out.Issues = r.Issues
		}
	}
	return json.MarshalIndent(out, "", "  ")
}

// ---------- 小工具 ----------

// defaultPlatform 返回 GOOS/GOARCH 形式的平台标识。
func defaultPlatform() string {
	return goruntime.GOOS + "/" + goruntime.GOARCH
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// compareSemver 比较 X.Y.Z[-pre]，只比较数值部分（预发布视为更低，数值相同则按原文比较）。
func compareSemver(a, b string) int {
	pa, preA := splitSemver(a)
	pb, preB := splitSemver(b)
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	if preA == preB {
		return 0
	}
	if preA == "" {
		return 1
	}
	if preB == "" {
		return -1
	}
	return strings.Compare(preA, preB)
}

func splitSemver(v string) ([3]int, string) {
	var out [3]int
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out, pre
}

func normalizeVer(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
