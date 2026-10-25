// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"
)

// writeSourceDir 在 dir 下写入一份可直接安装的 K1 源目录（manifest + 签名 + 可执行入口）。
func writeSourceDir(t *testing.T, dir, id, version string, kind Kind, mutate func(map[string]any), sign bool) string {
	t.Helper()
	m := baseManifestMap(id, version, kind)
	if mutate != nil {
		mutate(m)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	raw := mustJSON(t, m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
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
	if kind == KindK1 {
		binDir := filepath.Join(dir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir bin: %v", err)
		}
		script := "#!/bin/sh\nsleep 30\n"
		if err := os.WriteFile(filepath.Join(binDir, id), []byte(script), 0o755); err != nil {
			t.Fatalf("write entrypoint: %v", err)
		}
	}
	return dir
}

func newInstaller(t *testing.T, root string) *Installer {
	t.Helper()
	return &Installer{L: Layout{Root: root}, Opts: testLoaderOptions(t)}
}

func TestStateSaveLoadAtomic(t *testing.T) {
	root := t.TempDir()
	l := Layout{Root: root}
	st := &State{PluginID: "pkg-a", ActiveVersion: "1.0.0", ActiveHash: "sha256:abc"}
	if err := SaveState(l, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(l, "pkg-a")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.ActiveVersion != "1.0.0" || got.ActiveHash != "sha256:abc" || got.UpdatedAt == "" {
		t.Fatalf("state mismatch: %+v", got)
	}
	if _, err := os.Stat(l.StatePath("pkg-a") + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary state file must not remain: %v", err)
	}
	// 不存在的插件：空状态且不报错
	empty, err := LoadState(l, "pkg-none")
	if err != nil || empty.ActiveVersion != "" {
		t.Fatalf("missing state: %+v err=%v", empty, err)
	}
}

func TestLayoutVersionsSorted(t *testing.T) {
	root := t.TempDir()
	l := Layout{Root: root}
	for _, ver := range []string{"1.2.0", "1.10.0", "1.9.0"} {
		writeSourceDir(t, filepath.Join(root, "pkg-b", ver), "pkg-b", ver, KindK1, nil, true)
	}
	got, err := l.VersionsOnDisk("pkg-b")
	if err != nil {
		t.Fatalf("VersionsOnDisk: %v", err)
	}
	want := []string{"1.2.0", "1.9.0", "1.10.0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("versions = %v, want %v", got, want)
		}
	}
}

func TestInstallActivateCommit(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeSourceDir(t, src, "pkg-c", "1.0.0", KindK1, nil, true)
	in := newInstaller(t, root)

	res, err := in.InstallVersion(src)
	if err != nil {
		t.Fatalf("InstallVersion: %v", err)
	}
	if res.Version != "1.0.0" || res.ManifestHash == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "manifest.json")); err != nil {
		t.Fatalf("installed manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "bin", "pkg-c")); err != nil {
		t.Fatalf("installed entrypoint missing: %v", err)
	}
	if err := in.Activate("pkg-c", res.Version, res.ManifestHash); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	st, _ := LoadState(in.L, "pkg-c")
	if st.ActiveVersion != "1.0.0" || st.Txn == nil || st.Txn.Phase != TxnPhaseUpgrading {
		t.Fatalf("after activate: %+v", st)
	}
	if err := in.Commit("pkg-c"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	st, _ = LoadState(in.L, "pkg-c")
	if st.Txn != nil || st.Previous != "" {
		t.Fatalf("after commit: %+v", st)
	}
	if len(st.History) == 0 {
		t.Fatal("history must record install/upgrade")
	}
}

func TestUpgradeThenRollback(t *testing.T) {
	root := t.TempDir()
	in := newInstaller(t, root)

	for _, ver := range []string{"1.0.0", "1.1.0"} {
		src := filepath.Join(t.TempDir(), "src")
		writeSourceDir(t, src, "pkg-d", ver, KindK1, nil, true)
		res, err := in.InstallVersion(src)
		if err != nil {
			t.Fatalf("install %s: %v", ver, err)
		}
		if err := in.Activate("pkg-d", res.Version, res.ManifestHash); err != nil {
			t.Fatalf("activate %s: %v", ver, err)
		}
		if ver == "1.0.0" {
			if err := in.Commit("pkg-d"); err != nil {
				t.Fatalf("commit v1: %v", err)
			}
		}
	}
	st, _ := LoadState(in.L, "pkg-d")
	if st.ActiveVersion != "1.1.0" || st.Previous != "1.0.0" {
		t.Fatalf("upgrade state: %+v", st)
	}
	if err := in.Rollback("pkg-d"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	st, _ = LoadState(in.L, "pkg-d")
	if st.ActiveVersion != "1.0.0" || st.Previous != "" || st.Txn != nil {
		t.Fatalf("after rollback: %+v", st)
	}
	if err := in.Rollback("pkg-d"); !errors.Is(err, ErrNoPreviousVersion) {
		t.Fatalf("second rollback must fail with ErrNoPreviousVersion, got %v", err)
	}
}

func TestInstallRejectsK0AndUnsigned(t *testing.T) {
	root := t.TempDir()
	in := newInstaller(t, root)

	k0 := filepath.Join(t.TempDir(), "k0")
	writeSourceDir(t, k0, "pkg-k0", "1.0.0", KindK0, nil, false)
	if _, err := in.InstallVersion(k0); err == nil {
		t.Fatal("K0 must be rejected")
	} else {
		var le *LoadError
		if !errors.As(err, &le) || le.Code != CodeK0OnDisk {
			t.Fatalf("want %s, got %v", CodeK0OnDisk, err)
		}
	}

	unsigned := filepath.Join(t.TempDir(), "unsigned")
	writeSourceDir(t, unsigned, "pkg-u", "1.0.0", KindK1, nil, false)
	if _, err := in.InstallVersion(unsigned); err == nil {
		t.Fatal("unsigned K1 must be rejected")
	}
}

func TestReconcileMatrix(t *testing.T) {
	cases := []struct {
		name    string
		phase   TxnPhase
		healthy bool
		want    ReconcileAction
	}{
		{"no-txn", "", false, ReconcileNone},
		{"staged", TxnPhaseStaged, false, ReconcileCleanup},
		{"upgrading-healthy", TxnPhaseUpgrading, true, ReconcileComplete},
		{"upgrading-unhealthy", TxnPhaseUpgrading, false, ReconcileRollback},
		{"rolling-back", TxnPhaseRollingBack, false, ReconcileRollback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{PluginID: "pkg-e"}
			if tc.phase != "" {
				st.Txn = &Txn{Phase: tc.phase}
			}
			got, err := Reconcile(Layout{Root: t.TempDir()}, st, tc.healthy)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got != tc.want {
				t.Fatalf("action = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSupervisorStartStop(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("requires /bin/sh and process groups")
	}
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeSourceDir(t, src, "pkg-s", "1.0.0", KindK1, func(m map[string]any) {
		m["health"] = map[string]any{"type": "exec", "target": "true", "timeout": "2s"}
		m["lifecycle"] = map[string]any{"startTimeout": "5s", "stopTimeout": "1s", "restart": "never"}
	}, true)
	in := newInstaller(t, root)
	res, err := in.InstallVersion(src)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	cmdPath, err := EntrypointCommand(res.Dir, m)
	if err != nil {
		t.Fatalf("EntrypointCommand: %v", err)
	}
	sup := NewSupervisor(ProcSpec{ID: "pkg-s", Version: "1.0.0", Dir: res.Dir,
		Command: cmdPath, Health: m.Health, Lifecycle: m.Lifecycle})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sup.Running() {
		t.Fatal("supervisor should be running")
	}
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.Running() {
		t.Fatal("supervisor should be stopped")
	}
}

func TestSupervisorStartTimeoutOnUnhealthy(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeSourceDir(t, src, "pkg-t", "1.0.0", KindK1, func(m map[string]any) {
		m["health"] = map[string]any{"type": "exec", "target": "false", "timeout": "1s"}
		m["lifecycle"] = map[string]any{"startTimeout": "1s", "stopTimeout": "1s", "restart": "never"}
	}, true)
	in := newInstaller(t, root)
	res, err := in.InstallVersion(src)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(res.Dir, "manifest.json"))
	m, _, _ := Parse(raw)
	cmdPath, _ := EntrypointCommand(res.Dir, m)
	sup := NewSupervisor(ProcSpec{ID: "pkg-t", Version: "1.0.0", Dir: res.Dir,
		Command: cmdPath, Health: m.Health, Lifecycle: m.Lifecycle})
	start := time.Now()
	if err := sup.Start(context.Background()); err == nil {
		_ = sup.Stop(context.Background())
		t.Fatal("Start must fail when health never passes")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("start timeout took too long: %s", elapsed)
	}
}
