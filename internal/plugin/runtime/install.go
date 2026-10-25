package pluginruntime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 安装/升级/回滚相关的具名错误。
var (
	ErrVersionExists     = errors.New("runtime: version already installed")
	ErrNoActiveVersion   = errors.New("runtime: plugin has no active version")
	ErrNoPreviousVersion = errors.New("runtime: plugin has no previous version")
	ErrNotInstalled      = errors.New("runtime: plugin not installed")
	ErrManifestNotFound  = errors.New("runtime: manifest.json not found in source dir")
)

// Installer 执行 K1 插件的安装/升级/回滚（设计 §6）。
type Installer struct {
	L    Layout
	Opts Options
}

// InstallResult 是一次安装/升级的结果。
type InstallResult struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	ManifestHash string `json:"manifestHash"`
	Dir          string `json:"dir"`
}

// candidate 是源目录里通过校验的候选版本。
type candidate struct {
	manifest *Manifest
	hash     string
	raw      []byte
}

// readCandidate 读取并校验源目录中的 manifest（schema/kind/签名/平台/内核版本）。
func (in *Installer) readCandidate(srcDir string) (*candidate, error) {
	manifestPath := filepath.Join(srcDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, srcDir)
		}
		return nil, &LoadError{Code: CodeIO, Path: manifestPath, Err: err}
	}
	m, hash, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}
	if !m.Kind.Installable() {
		return nil, &LoadError{Code: CodeK0OnDisk, Path: manifestPath,
			Message: "K0 plugins are compiled into the kernel and cannot be installed from disk; K0 upgrade = kernel rolling restart"}
	}
	if in.Opts.Trust == nil || in.Opts.Trust.Len() == 0 {
		return nil, &LoadError{Code: CodeTrustStoreUnavailable, Path: manifestPath,
			Message: "K1/K2 manifest requires a trust store", Err: ErrTrustStoreUnavailable}
	}
	sigPath := filepath.Join(srcDir, "manifest.sig")
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &LoadError{Code: CodeSignatureMissing, Path: manifestPath, Err: ErrSignatureMissing}
		}
		return nil, &LoadError{Code: CodeIO, Path: sigPath, Err: err}
	}
	if err := Verify(raw, sig, in.Opts.Trust); err != nil {
		code := CodeBadSignature
		switch SignatureState(err) {
		case SigMissing:
			code = CodeSignatureMissing
		case SigMalformed:
			code = CodeSignatureMalformed
		case SigUnknownKey:
			code = CodeUnknownPublisherKey
		}
		return nil, &LoadError{Code: code, Path: manifestPath, Err: err}
	}
	if len(m.Targets) > 0 && !containsString(m.Targets, in.Opts.platform()) {
		return nil, &LoadError{Code: CodeUnsupportedPlatform, Path: manifestPath,
			Message: fmt.Sprintf("targets %v do not include %s", m.Targets, in.Opts.platform())}
	}
	if m.MinKernelVersion != "" && in.Opts.KernelVersion != "" &&
		compareSemver(normalizeVer(in.Opts.KernelVersion), normalizeVer(m.MinKernelVersion)) < 0 {
		return nil, &LoadError{Code: CodeKernelTooOld, Path: manifestPath,
			Message: fmt.Sprintf("kernel %s < minKernelVersion %s", in.Opts.KernelVersion, m.MinKernelVersion)}
	}
	return &candidate{manifest: m, hash: hash, raw: raw}, nil
}

// InstallVersion 把源目录安装到 <root>/<id>/<version>/（不激活）。
// 目录改名是原子的：先完整拷到 staging，再 rename 到位（设计 §6.1）。
func (in *Installer) InstallVersion(srcDir string) (*InstallResult, error) {
	c, err := in.readCandidate(srcDir)
	if err != nil {
		return nil, err
	}
	id, ver := c.manifest.ID, c.manifest.Version
	if err := in.L.Ensure(id); err != nil {
		return nil, err
	}
	dst := in.L.VersionDir(id, ver)
	if _, statErr := os.Stat(dst); statErr == nil {
		return nil, fmt.Errorf("%w: %s@%s", ErrVersionExists, id, ver)
	}
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	staging := filepath.Join(in.L.StagingDir(id), ver+"."+suffix)
	defer os.RemoveAll(staging)
	if err := copyTree(srcDir, staging); err != nil {
		return nil, fmt.Errorf("runtime: stage %s@%s: %w", id, ver, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, dst); err != nil {
		return nil, fmt.Errorf("runtime: activate version dir %s: %w", dst, err)
	}
	return &InstallResult{ID: id, Version: ver, ManifestHash: c.hash, Dir: dst}, nil
}

// Begin 记录"事务开始"（staged），是安装/升级的第一步。
func (in *Installer) Begin(id, fromVersion, fromHash, toVersion, toHash string, phase TxnPhase) error {
	st, err := LoadState(in.L, id)
	if err != nil {
		return err
	}
	st.Txn = &Txn{Phase: phase, FromVersion: fromVersion, FromHash: fromHash,
		ToVersion: toVersion, ToHash: toHash, StartedAt: nowRFC3339()}
	return SaveState(in.L, st)
}

// Activate 把指定版本设为 active，并把原 active 记为 previous（升级语义）。
// 调用方须在健康检查通过后调用 Commit；失败则调用 Rollback。
func (in *Installer) Activate(id, version, manifestHash string) error {
	st, err := LoadState(in.L, id)
	if err != nil {
		return err
	}
	if version == "" {
		return fmt.Errorf("%w: empty version", ErrNoActiveVersion)
	}
	from := st.ActiveVersion
	fromHash := st.ActiveHash
	if from != "" && from != version {
		st.Previous = from
	}
	st.ActiveVersion = version
	st.ActiveHash = manifestHash
	phase := TxnPhaseUpgrading
	if from == "" {
		phase = TxnPhaseUpgrading // 首次安装同样以 upgrading 表示"待健康确认"
	}
	st.Txn = &Txn{Phase: phase, FromVersion: from, FromHash: fromHash,
		ToVersion: version, ToHash: manifestHash, StartedAt: nowRFC3339()}
	appendHistory(st, HistoryEntry{Version: version, Hash: manifestHash,
		Action: "install", At: nowRFC3339(), Prev: from})
	return SaveState(in.L, st)
}

// Commit 结束升级事务（健康通过后的落盘点）。
func (in *Installer) Commit(id string) error {
	st, err := LoadState(in.L, id)
	if err != nil {
		return err
	}
	if st.Txn == nil {
		return nil
	}
	action := "install"
	if st.Txn.FromVersion != "" {
		action = "upgrade"
	}
	st.Previous = ""
	st.Txn = nil
	appendHistory(st, HistoryEntry{Version: st.ActiveVersion, Hash: st.ActiveHash,
		Action: action, At: nowRFC3339(), Prev: ""})
	return SaveState(in.L, st)
}

// Rollback 把 active 切回 previous（设计 §6.2：禁止自动再次回滚，防抖动）。
func (in *Installer) Rollback(id string) error {
	st, err := LoadState(in.L, id)
	if err != nil {
		return err
	}
	if st.Previous == "" {
		return fmt.Errorf("%w: %s", ErrNoPreviousVersion, id)
	}
	to := st.Previous
	st.Txn = &Txn{Phase: TxnPhaseRollingBack, FromVersion: st.ActiveVersion,
		FromHash: st.ActiveHash, ToVersion: to, StartedAt: nowRFC3339()}
	if err := SaveState(in.L, st); err != nil {
		return err
	}
	st.ActiveVersion = to
	st.ActiveHash = ""
	st.Previous = ""
	st.Txn = nil
	appendHistory(st, HistoryEntry{Version: to, Action: "rollback", At: nowRFC3339()})
	return SaveState(in.L, st)
}

// Uninstall 删除某插件的全部版本目录与状态（调用方须先停止进程）。
func (in *Installer) Uninstall(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	dir := in.L.PluginDir(id)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotInstalled, id)
		}
		return err
	}
	return os.RemoveAll(dir)
}

// Prune 只保留最新的 keep 个版本（keep<=0 视为 1），删除其余版本目录。
func (in *Installer) Prune(id string, keep int) ([]string, error) {
	if keep <= 0 {
		keep = 1
	}
	versions, err := in.L.VersionsOnDisk(id)
	if err != nil {
		return nil, err
	}
	if len(versions) <= keep {
		return nil, nil
	}
	st, err := LoadState(in.L, id)
	if err != nil {
		return nil, err
	}
	drop := versions[:len(versions)-keep]
	var removed []string
	for _, ver := range drop {
		if ver == st.ActiveVersion || ver == st.Previous {
			continue
		}
		if err := os.RemoveAll(in.L.VersionDir(id, ver)); err != nil {
			return removed, err
		}
		removed = append(removed, ver)
	}
	return removed, nil
}

// ActiveVersion 返回当前 active 版本（空表示未激活）。
func (in *Installer) ActiveVersion(id string) (string, error) {
	st, err := LoadState(in.L, id)
	if err != nil {
		return "", err
	}
	return st.ActiveVersion, nil
}

func randomSuffix() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// copyTree 递归复制目录，保留文件权限位（K1 入口通常是可执行文件）。
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil // 跳过符号链接/设备等，避免把宿主路径带进插件目录
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// EntrypointCommand 从 manifest 的 entrypoint 解析可执行文件路径（K1 要求 exec + 安全相对路径）。
func EntrypointCommand(versionDir string, m *Manifest) (string, error) {
	if m == nil {
		return "", errors.New("runtime: nil manifest")
	}
	if m.Kind != KindK1 {
		return "", fmt.Errorf("runtime: kind %s has no executable entrypoint", m.Kind)
	}
	if strings.TrimSpace(m.Entrypoint.Path) == "" {
		return "", errors.New("runtime: manifest entrypoint.path is empty")
	}
	if !safeRelPath(m.Entrypoint.Path) {
		return "", fmt.Errorf("runtime: entrypoint.path %q is not a safe relative path", m.Entrypoint.Path)
	}
	cmd := filepath.Join(versionDir, filepath.FromSlash(m.Entrypoint.Path))
	if _, err := os.Stat(cmd); err != nil {
		return "", fmt.Errorf("runtime: entrypoint %s: %w", cmd, err)
	}
	return cmd, nil
}
