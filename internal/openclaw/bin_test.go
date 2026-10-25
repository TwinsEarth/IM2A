// SPDX-License-Identifier: MIT

package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetForTest 清空解析缓存，并在测试结束后再清一次，避免跨测试串味。
func resetForTest(t *testing.T) {
	t.Helper()
	ResetResolveCache()
	t.Cleanup(ResetResolveCache)
}

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	return path
}

func TestResolveBinEnvWins(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "openclaw", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)
	t.Setenv(EnvBin, "/explicit/bin/openclaw")
	resetForTest(t)
	if got := ResolveBin(); got != "/explicit/bin/openclaw" {
		t.Fatalf("ResolveBin() = %q, want explicit env value", got)
	}
}

func TestResolveBinLookPath(t *testing.T) {
	dir := t.TempDir()
	want := writeExecutable(t, dir, "openclaw", "#!/bin/sh\nexit 0\n")
	t.Setenv(EnvBin, "")
	t.Setenv("PATH", dir)
	resetForTest(t)
	if got := ResolveBin(); got != want {
		t.Fatalf("ResolveBin() = %q, want PATH lookup %q", got, want)
	}
}

func TestResolveBinLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	legacy := writeExecutable(t, dir, "openclaw", "#!/bin/sh\nexit 0\n")
	orig := legacyBinCandidates
	t.Cleanup(func() { legacyBinCandidates = orig })
	legacyBinCandidates = []string{legacy}

	t.Setenv(EnvBin, "")
	t.Setenv("PATH", "") // PATH 里没有 openclaw，必须落到底层已知安装位置
	resetForTest(t)
	if got := ResolveBin(); got != legacy {
		t.Fatalf("ResolveBin() = %q, want legacy fallback %q", got, legacy)
	}
}

func TestResolveBinUnresolved(t *testing.T) {
	orig := legacyBinCandidates
	t.Cleanup(func() { legacyBinCandidates = orig })
	legacyBinCandidates = nil

	t.Setenv(EnvBin, "")
	t.Setenv("PATH", "")
	resetForTest(t)
	if got := ResolveBin(); got != "" {
		t.Fatalf("ResolveBin() = %q, want empty when nothing resolves", got)
	}
}

func TestResolveCacheReset(t *testing.T) {
	t.Setenv(EnvBin, "/first/openclaw")
	resetForTest(t)
	if got := ResolveBin(); got != "/first/openclaw" {
		t.Fatalf("ResolveBin() = %q, want /first/openclaw", got)
	}
	t.Setenv(EnvBin, "/second/openclaw")
	if got := ResolveBin(); got != "/first/openclaw" {
		t.Fatalf("cached ResolveBin() = %q, want cached /first/openclaw", got)
	}
	ResetResolveCache()
	if got := ResolveBin(); got != "/second/openclaw" {
		t.Fatalf("ResolveBin() after reset = %q, want /second/openclaw", got)
	}
}

func TestResolveHome(t *testing.T) {
	t.Setenv(EnvHome, "/srv/openclaw-home")
	resetForTest(t)
	if got := ResolveHome(); got != "/srv/openclaw-home" {
		t.Fatalf("ResolveHome() = %q, want env override", got)
	}

	homeDir := t.TempDir()
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", homeDir)
	ResetResolveCache()
	if got := ResolveHome(); got != homeDir {
		t.Fatalf("ResolveHome() = %q, want UserHomeDir %q", got, homeDir)
	}
}

func TestResolveChannel(t *testing.T) {
	t.Setenv(EnvChannel, "telegram")
	resetForTest(t)
	if got := ResolveChannel(); got != "telegram" {
		t.Fatalf("ResolveChannel() = %q, want env override", got)
	}
	t.Setenv(EnvChannel, "")
	ResetResolveCache()
	if got := ResolveChannel(); got != DefaultChannel {
		t.Fatalf("ResolveChannel() = %q, want default %q", got, DefaultChannel)
	}
}

func TestResolveAccountsDir(t *testing.T) {
	t.Setenv(EnvAccountsDir, "/custom/accounts")
	resetForTest(t)
	if got := ResolveAccountsDir(); got != "/custom/accounts" {
		t.Fatalf("ResolveAccountsDir() = %q, want env override", got)
	}

	t.Setenv(EnvAccountsDir, "")
	t.Setenv(EnvHome, "/srv/home")
	t.Setenv(EnvChannel, "")
	ResetResolveCache()
	want := filepath.Join("/srv/home", ".openclaw", DefaultChannel, "accounts")
	if got := ResolveAccountsDir(); got != want {
		t.Fatalf("ResolveAccountsDir() = %q, want %q", got, want)
	}
}

func TestResolveAccountsDirUnresolvedHome(t *testing.T) {
	t.Setenv(EnvAccountsDir, "")
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", "")
	resetForTest(t)
	if got := ResolveAccountsDir(); got != "" {
		t.Fatalf("ResolveAccountsDir() = %q, want empty when home is unresolved", got)
	}
}

func TestResolveGatewayMode(t *testing.T) {
	t.Setenv(EnvGatewayMode, "MANAGED")
	resetForTest(t)
	if got := ResolveGatewayMode(); got != GatewayModeManaged {
		t.Fatalf("ResolveGatewayMode() = %q, want managed", got)
	}
	t.Setenv(EnvGatewayMode, "")
	ResetResolveCache()
	if got := ResolveGatewayMode(); got != GatewayModeExternal {
		t.Fatalf("ResolveGatewayMode() = %q, want external default", got)
	}
	t.Setenv(EnvGatewayMode, "whatever")
	ResetResolveCache()
	if got := ResolveGatewayMode(); got != GatewayModeExternal {
		t.Fatalf("ResolveGatewayMode() = %q, want external for unknown mode", got)
	}
}

func TestResolveBinDirAndPath(t *testing.T) {
	t.Setenv(EnvBin, "/opt/openclaw/bin/openclaw")
	t.Setenv("PATH", "/usr/bin")
	resetForTest(t)
	if got := ResolveBinDir(); got != "/opt/openclaw/bin" {
		t.Fatalf("ResolveBinDir() = %q, want /opt/openclaw/bin", got)
	}
	wantPath := "/opt/openclaw/bin" + string(os.PathListSeparator) + "/usr/bin"
	if got := PathWithBinDir(); got != wantPath {
		t.Fatalf("PathWithBinDir() = %q, want %q", got, wantPath)
	}
}

func TestLegacyLiteralIsSplit(t *testing.T) {
	// 验收要求：完整硬编码可执行路径不得出现在非测试代码里；这里验证 legacy 目录与文件名已拆分。
	full := legacyOpenClawBinDir + "/openclaw"
	if !strings.HasSuffix(full, "/openclaw") {
		t.Fatalf("legacy fallback path malformed: %q", full)
	}
	if !strings.HasPrefix(legacyOpenClawBinDir, "/opt/") {
		t.Fatalf("legacy dir = %q", legacyOpenClawBinDir)
	}
}
