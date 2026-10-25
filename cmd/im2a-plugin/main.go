// SPDX-License-Identifier: MIT

// Command im2a-plugin 是 K1 插件的运维入口（安装/升级/回滚/状态/卸载/清理）。
//
// 设计约束（PLUGIN-RUNTIME-V2-DESIGN §6）：
//   - 真正的安装/升级/回滚只发生在 K1（独立进程）；K0 的"升级"= 内核滚动重启，
//     因此 K0 请求 install/upgrade/rollback 一律返回 E_K0_NOT_INSTALLABLE。
//   - 升级采用两阶段：install/activate（txn=upgrading）→ 由 supervisor 健康检查后 commit；
//     失败则 rollback。CLI 只负责落盘状态，不代持进程生命周期。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	pluginruntime "human-agent-im/internal/plugin/runtime"
)

const defaultRoot = "/opt/im2a/plugins"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "list":
		err = cmdList(args)
	case "status":
		err = cmdStatus(args)
	case "install":
		err = cmdInstall(args, false)
	case "upgrade":
		err = cmdInstall(args, true)
	case "commit":
		err = cmdCommit(args)
	case "rollback":
		err = cmdRollback(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "prune":
		err = cmdPrune(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown subcommand %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "im2a-plugin:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `im2a-plugin — K1 plugin operations (IM2A runtime v2)

usage:
  im2a-plugin list     -root DIR
  im2a-plugin status   -root DIR -id ID
  im2a-plugin install  -root DIR -trust DIR -src DIR
  im2a-plugin upgrade  -root DIR -trust DIR -src DIR
  im2a-plugin commit   -root DIR -id ID
  im2a-plugin rollback -root DIR -id ID
  im2a-plugin uninstall -root DIR -id ID
  im2a-plugin prune    -root DIR -id ID [-keep N]

notes:
  K0 plugins cannot be installed from disk: K0 upgrade = kernel rolling restart.
  install/upgrade only write state (txn=upgrading); run health checks then commit or rollback.
`)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

func rootFlag(fs *flag.FlagSet) *string {
	return fs.String("root", defaultRoot, "plugin root directory")
}

func trustFlag(fs *flag.FlagSet) *string {
	return fs.String("trust", "", "trusted publisher keys directory (PEM)")
}

func idFlag(fs *flag.FlagSet) *string { return fs.String("id", "", "plugin id") }
func srcFlag(fs *flag.FlagSet) *string {
	return fs.String("src", "", "source directory containing manifest.json + manifest.sig")
}

func cmdList(args []string) error {
	fs := newFlagSet("list")
	root := rootFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := os.ReadDir(*root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("no plugins installed under %s\n", *root)
			return nil
		}
		return err
	}
	layout := pluginruntime.Layout{Root: *root}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		fmt.Printf("no plugins installed under %s\n", *root)
		return nil
	}
	for _, id := range ids {
		st, err := pluginruntime.LoadState(layout, id)
		if err != nil {
			fmt.Printf("%-24s state-error=%v\n", id, err)
			continue
		}
		fmt.Printf("%-24s active=%-10s previous=%-10s txn=%s\n", id, dash(st.ActiveVersion), dash(st.Previous), txnPhase(st))
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := newFlagSet("status")
	root, id := rootFlag(fs), idFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	layout := pluginruntime.Layout{Root: *root}
	st, err := pluginruntime.LoadState(layout, *id)
	if err != nil {
		return err
	}
	versions, _ := layout.VersionsOnDisk(*id)
	out := map[string]any{
		"pluginId":        st.PluginID,
		"activeVersion":   st.ActiveVersion,
		"previousVersion": st.Previous,
		"activeHash":      st.ActiveHash,
		"txn":             st.Txn,
		"versionsOnDisk":  versions,
		"grants":          st.Grants,
		"history":         st.History,
		"updatedAt":       st.UpdatedAt,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdInstall(args []string, upgrade bool) error {
	name := "install"
	if upgrade {
		name = "upgrade"
	}
	fs := newFlagSet(name)
	root, trust := rootFlag(fs), trustFlag(fs)
	src, id := srcFlag(fs), idFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src == "" {
		return errors.New("-src is required")
	}
	if *trust == "" {
		return errors.New("-trust is required (K1 manifest signature must be verified)")
	}
	store, err := pluginruntime.LoadTrustDir(*trust)
	if err != nil {
		return fmt.Errorf("load trust dir: %w", err)
	}
	in := &pluginruntime.Installer{L: pluginruntime.Layout{Root: *root},
		Opts: pluginruntime.Options{Trust: store}}
	res, err := in.InstallVersion(*src)
	if err != nil {
		return err
	}
	if *id != "" && *id != res.ID {
		return fmt.Errorf("manifest id %q does not match -id %q", res.ID, *id)
	}
	st, err := pluginruntime.LoadState(in.L, res.ID)
	if err != nil {
		return err
	}
	from, fromHash := st.ActiveVersion, st.ActiveHash
	if err := in.Begin(res.ID, from, fromHash, res.Version, res.ManifestHash, pluginruntime.TxnPhaseStaged); err != nil {
		return err
	}
	if err := in.Activate(res.ID, res.Version, res.ManifestHash); err != nil {
		return err
	}
	fmt.Printf("%s: %s@%s installed (manifest=%s)\n", name, res.ID, res.Version, res.ManifestHash)
	fmt.Printf("state: txn=upgrading (from=%s to=%s)\n", dash(from), res.Version)
	fmt.Printf("next: verify health, then `im2a-plugin commit -root %s -id %s` or `rollback`\n", *root, res.ID)
	return nil
}

func cmdCommit(args []string) error {
	fs := newFlagSet("commit")
	root, id := rootFlag(fs), idFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	in := &pluginruntime.Installer{L: pluginruntime.Layout{Root: *root}}
	if err := in.Commit(*id); err != nil {
		return err
	}
	fmt.Printf("commit: %s healthy, upgrade finalized\n", *id)
	return nil
}

func cmdRollback(args []string) error {
	fs := newFlagSet("rollback")
	root, id := rootFlag(fs), idFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	in := &pluginruntime.Installer{L: pluginruntime.Layout{Root: *root}}
	if err := in.Rollback(*id); err != nil {
		return err
	}
	fmt.Printf("rollback: %s switched back to previous version\n", *id)
	return nil
}

func cmdUninstall(args []string) error {
	fs := newFlagSet("uninstall")
	root, id := rootFlag(fs), idFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	in := &pluginruntime.Installer{L: pluginruntime.Layout{Root: *root}}
	if err := in.Uninstall(*id); err != nil {
		return err
	}
	fmt.Printf("uninstall: %s removed (stop its process first)\n", *id)
	return nil
}

func cmdPrune(args []string) error {
	fs := newFlagSet("prune")
	root, id := rootFlag(fs), idFlag(fs)
	keep := fs.Int("keep", 2, "number of versions to keep")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	in := &pluginruntime.Installer{L: pluginruntime.Layout{Root: *root}}
	removed, err := in.Prune(*id, *keep)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		fmt.Printf("prune: %s nothing to remove (keep=%d)\n", *id, *keep)
		return nil
	}
	fmt.Printf("prune: %s removed %v\n", *id, removed)
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func txnPhase(st *pluginruntime.State) string {
	if st.Txn == nil {
		return "-"
	}
	return string(st.Txn.Phase)
}

var _ = filepath.Join // keep filepath import if future flags use paths
