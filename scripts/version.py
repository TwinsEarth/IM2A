#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""IM2A version single-source generator/validator.

Authoritative sources:
  * core version      : internal/version/version.go  (const Version = "vX.Y.Z")
  * edition matrix    : editions.json                (independent per-edition versions)

Generated targets (never edit by hand; run `make version-sync`):
  * README.md                       - core version badge + generated editions table
  * npm/package.json                - core version (CLI launcher), license MIT
  * npm/bin/im2a.js                 - PKG_VERSION launcher constant (core version)
  * client/app/pubspec.yaml         - client edition version
  * deploy/Dockerfile               - ARG VERSION has no default (injected via --build-arg)
  * packages/openclaw-channel-relation/package.json - edition version
  * Makefile                        - VERSION derived from this source (checked only)

Usage: scripts/version.sh {plain|semver|table|sync|check}
       (the shell wrapper resolves --root and requires python3)
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

CORE_SRC = "internal/version/version.go"
EDITIONS_JSON = "editions.json"
README = "README.md"
NPM_PKG = "npm/package.json"
NPM_BIN = "npm/bin/im2a.js"
PUBSPEC = "client/app/pubspec.yaml"
DOCKERFILE = "deploy/Dockerfile"
PLUGIN_PKG = "packages/openclaw-channel-relation/package.json"
MAKEFILE = "Makefile"

BEGIN = "<!-- BEGIN GENERATED:editions (make version-sync) -->"
END = "<!-- END GENERATED:editions -->"

SEMVER_RE = re.compile(r"^\d+\.\d+\.\d+$")
RANGE_RE = re.compile(r"^>=\d+\.\d+\.\d+ <\d+\.\d+\.\d+$")


def fail(msg: str) -> None:
    print(f"[version] FAIL: {msg}", file=sys.stderr)


def read_core(root: Path) -> str:
    src = (root / CORE_SRC).read_text(encoding="utf-8")
    m = re.search(r'^const Version = "(v\d+\.\d+\.\d+)"$', src, re.M)
    if not m:
        raise SystemExit(f"[version] FATAL: cannot parse Version from {CORE_SRC}")
    return m.group(1)


def read_editions(root: Path) -> dict:
    try:
        data = json.loads((root / EDITIONS_JSON).read_text(encoding="utf-8"))
    except FileNotFoundError:
        raise SystemExit(f"[version] FATAL: missing {EDITIONS_JSON}")
    if "core" not in data or "editions" not in data:
        raise SystemExit("[version] FATAL: editions.json needs 'core' and 'editions'")
    return data


def find_edition_with_version_file(data: dict, version_file: str) -> dict | None:
    for e in data["editions"]:
        if e.get("versionFile") == version_file:
            return e
    return None


def parse_semver(v: str) -> tuple[int, int, int]:
    return tuple(int(x) for x in v.split("."))  # type: ignore[return-value]


def in_range(version: str, rng: str) -> bool:
    lo, hi = rng.split(" ")
    lo_v = lo[2:]
    hi_v = hi[1:]
    return parse_semver(lo_v) <= parse_semver(version) < parse_semver(hi_v)


def render_table(root: Path) -> str:
    data = read_editions(root)
    core = read_core(root)
    rows = [
        "| Edition | 版本 | 兼容 core | 产物 | 发布命令 |",
        "|---|---|---|---|---|",
        f"| `core`（服务端 Go） | {core} | — | `{data['core'].get('artifactPath', 'services/gateway')}` | `{data['core'].get('publishCommand', 'make release-build')}` |",
    ]
    for e in data["editions"]:
        rows.append(
            "| `{id}` | {ver} | {core_range} | `{path}` | `{cmd}` |".format(
                id=e["id"],
                ver=e["version"],
                core_range=e.get("compatibleCore", "—"),
                path=e.get("artifactPath", "—"),
                cmd=e.get("publishCommand", "—"),
            )
        )
    return BEGIN + "\n" + "\n".join(rows) + "\n" + END


def replace_generated_block(text: str, block: str) -> str:
    pattern = re.compile(re.escape(BEGIN) + r".*?" + re.escape(END), re.S)
    if not pattern.search(text):
        raise SystemExit("[version] FATAL: README.md is missing the generated editions block markers")
    return pattern.sub(block, text)


def set_pkg_version(path: Path, version: str, *, license_mit: bool = False, add_license_file: bool = False) -> None:
    data = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
    data["version"] = version
    if license_mit:
        data["license"] = "MIT"
    if add_license_file:
        files = data.setdefault("files", [])
        if "LICENSE" not in files:
            files.append("LICENSE")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def sync(root: Path) -> None:
    core = read_core(root)
    semver = core[1:]
    data = read_editions(root)

    # 1) README: badge + generated editions table
    readme = (root / README).read_text(encoding="utf-8")
    readme = re.sub(r"version-v\d+\.\d+\.\d+-blue", f"version-{core}-blue", readme)
    readme = replace_generated_block(readme, render_table(root))
    (root / README).write_text(readme, encoding="utf-8")

    # 2) npm package (core CLI)
    set_pkg_version(root / NPM_PKG, semver, license_mit=True, add_license_file=True)

    # 3) npm launcher constant
    npmbin = root / NPM_BIN
    if npmbin.exists():
        txt = npmbin.read_text(encoding="utf-8")
        txt = re.sub(r"const PKG_VERSION = '[^']*';", f"const PKG_VERSION = '{semver}';", txt)
        npmbin.write_text(txt, encoding="utf-8")

    # 4) client Flutter pubspec (client edition)
    client = find_edition_with_version_file(data, PUBSPEC)
    if client is None:
        raise SystemExit(f"[version] FATAL: no edition declares versionFile={PUBSPEC}")
    pub = root / PUBSPEC
    pub_txt = pub.read_text(encoding="utf-8")
    pub_txt = re.sub(r"^version: .*$", f"version: {client['version']}", pub_txt, flags=re.M)
    pub.write_text(pub_txt, encoding="utf-8")

    # 5) Dockerfile: ARG VERSION must have no default (injected at build time)
    df = root / DOCKERFILE
    if df.exists():
        df_txt = df.read_text(encoding="utf-8")
        df_txt = re.sub(r"^ARG VERSION=.*$", "ARG VERSION", df_txt, flags=re.M)
        df.write_text(df_txt, encoding="utf-8")

    # 6) OpenClaw channel-relation plugin package (version field only; body owned by plugin-dev)
    plugin = find_edition_with_version_file(data, PLUGIN_PKG)
    if plugin is not None:
        set_pkg_version(root / PLUGIN_PKG, plugin["version"], license_mit=True)

    print(f"[version] synced core={core} semver={semver}")
    for e in data["editions"]:
        print(f"[version]   edition {e['id']}: {e['version']} (core {e.get('compatibleCore', '—')})")


def check(root: Path) -> int:
    core = read_core(root)
    semver = core[1:]
    data = read_editions(root)
    bad = 0

    # editions.json structural checks
    ids = [e.get("id") for e in data["editions"]]
    if len(ids) != len(set(ids)):
        fail("editions.json has duplicate edition ids"); bad += 1
    for e in data["editions"]:
        if not SEMVER_RE.match(str(e.get("version", ""))):
            fail(f"edition {e.get('id')}: version is not semver: {e.get('version')}"); bad += 1
        rng = e.get("compatibleCore", "")
        if not RANGE_RE.match(str(rng)):
            fail(f"edition {e.get('id')}: compatibleCore must look like '>=x.y.z <a.b.c': {rng}"); bad += 1
        elif not in_range(semver, rng):
            fail(f"edition {e.get('id')}: version range {rng} does not include core {semver}"); bad += 1
        if not (root / str(e.get("artifactPath", ""))).exists():
            fail(f"edition {e.get('id')}: artifactPath not found: {e.get('artifactPath')}"); bad += 1
    if not (root / str(data["core"].get("versionSource", ""))).exists():
        fail("editions.json core.versionSource not found"); bad += 1

    # 1) README badge + generated table
    readme = (root / README).read_text(encoding="utf-8")
    if f"version-{core}-blue" not in readme:
        fail(f"README badge is not version-{core}-blue (run make version-sync)"); bad += 1
    try:
        expected = render_table(root)
        actual = re.search(re.escape(BEGIN) + r".*?" + re.escape(END), readme, re.S)
        if not actual or actual.group(0) != expected:
            fail("README generated editions table is stale (run make version-sync)"); bad += 1
    except SystemExit as exc:
        fail(str(exc)); bad += 1

    # 2) npm package.json (core)
    try:
        npm = json.loads((root / NPM_PKG).read_text(encoding="utf-8"))
        if npm.get("version") != semver:
            fail(f"npm/package.json version={npm.get('version')} want {semver}"); bad += 1
        if npm.get("license") != "MIT":
            fail("npm/package.json license != MIT"); bad += 1
        if "LICENSE" not in npm.get("files", []):
            fail("npm/package.json files[] must include LICENSE"); bad += 1
    except Exception as exc:  # noqa: BLE001
        fail(f"npm/package.json invalid: {exc}"); bad += 1

    # 3) npm launcher constant
    npmbin = root / NPM_BIN
    if npmbin.exists():
        m = re.search(r"const PKG_VERSION = '([^']*)';", npmbin.read_text(encoding="utf-8"))
        if not m or m.group(1) != semver:
            fail(f"npm/bin/im2a.js PKG_VERSION={m.group(1) if m else '?'} want {semver}"); bad += 1

    # 4) client pubspec
    client = find_edition_with_version_file(data, PUBSPEC)
    if client is None:
        fail(f"no edition declares versionFile={PUBSPEC}"); bad += 1
    else:
        m = re.search(r"^version: (.*)$", (root / PUBSPEC).read_text(encoding="utf-8"), re.M)
        got = m.group(1).strip() if m else "?"
        if got != client["version"]:
            fail(f"client/app/pubspec.yaml version={got} want {client['version']} (edition {client['id']})"); bad += 1

    # 5) Dockerfile: no default ARG VERSION
    df = root / DOCKERFILE
    if df.exists():
        txt = df.read_text(encoding="utf-8")
        if re.search(r"^ARG VERSION=", txt, re.M):
            fail("deploy/Dockerfile must use 'ARG VERSION' without a default (inject via --build-arg)"); bad += 1
        if not re.search(r"^ARG VERSION$", txt, re.M):
            fail("deploy/Dockerfile missing 'ARG VERSION'"); bad += 1

    # 6) plugin package
    plugin = find_edition_with_version_file(data, PLUGIN_PKG)
    if plugin is not None:
        try:
            pj = json.loads((root / PLUGIN_PKG).read_text(encoding="utf-8"))
            if pj.get("version") != plugin["version"]:
                fail(f"packages/openclaw-channel-relation/package.json version={pj.get('version')} want {plugin['version']}"); bad += 1
            if pj.get("license") != "MIT":
                fail("packages/openclaw-channel-relation/package.json license != MIT"); bad += 1
        except Exception as exc:  # noqa: BLE001
            fail(f"packages/openclaw-channel-relation/package.json invalid: {exc}"); bad += 1

    # 7) Makefile derives VERSION from the single source (no hardcoded literal)
    mk = (root / MAKEFILE).read_text(encoding="utf-8")
    if "scripts/version.sh plain" not in mk:
        fail("Makefile VERSION must derive from scripts/version.sh plain"); bad += 1
    if re.search(r"^VERSION\s*\?=\s*v\d+\.\d+\.\d+", mk, re.M):
        fail("Makefile still hardcodes VERSION"); bad += 1

    # 8a) stale conflicting values in managed non-README files (negative-drift guard)
    managed = [MAKEFILE, DOCKERFILE, NPM_PKG, PUBSPEC, PLUGIN_PKG, EDITIONS_JSON]
    for rel in managed:
        p = root / rel
        if not p.exists():
            continue
        for lineno, line in enumerate(p.read_text(encoding="utf-8").splitlines(), 1):
            if re.search(r"\bv1\.8\.8\b|\bv1\.9\.5\b", line):
                fail(f"{rel}:{lineno}: stale version literal: {line.strip()}"); bad += 1

    # 8b) README: only stale *current version* claims are forbidden. The release-history
    # tables (rows starting with "| v<version> |") are archival and legitimately mention old versions.
    for lineno, line in enumerate(readme.splitlines(), 1):
        if re.search(r"v2\.1\.1", line) and re.search(r"(当前|已交付（稳定版）)", line):
            fail(f"{README}:{lineno}: stale 'current version' claim: {line.strip()}"); bad += 1

    # 9) authoritative core version literal only in version.go (+ generated README badge/table).
    # README 的"版本历史表"（行如 `| v2.0.2 | ... |`）是归档记录，允许出现任意历史版本号，
    # 包括恰好等于当前 core 版本的字面量（例如把新主线版本定为 v2.0.2 时）。
    history_row = re.compile(r"^\|\s*v\d+\.\d+\.\d+\s*\|")
    for rel in managed + [README, "EDITIONS.md"]:
        p = root / rel
        if not p.exists():
            continue
        for lineno, line in enumerate(p.read_text(encoding="utf-8").splitlines(), 1):
            if core not in line:
                continue
            if rel == README and ("img.shields.io/badge/version-" in line or line.strip().startswith("| `core`")):
                continue
            if rel == README and history_row.match(line.strip()):
                continue
            fail(f"{rel}:{lineno}: core version literal {core} outside {CORE_SRC}/generated targets"); bad += 1

    if bad:
        print(f"[version] CHECK FAILED: {bad} problem(s); run `make version-sync` after editing {CORE_SRC} / {EDITIONS_JSON}", file=sys.stderr)
        return 1
    print(f"[version] CHECK OK: core={core}; editions={len(data['editions'])}")
    return 0


def main() -> int:
    import argparse

    ap = argparse.ArgumentParser()
    ap.add_argument("--root", required=True)
    ap.add_argument("action", choices=["plain", "semver", "table", "sync", "check"])
    args = ap.parse_args()
    root = Path(args.root).resolve()

    if args.action == "plain":
        print(read_core(root))
        return 0
    if args.action == "semver":
        print(read_core(root)[1:])
        return 0
    if args.action == "table":
        print(render_table(root))
        return 0
    if args.action == "sync":
        sync(root)
        return 0
    return check(root)


if __name__ == "__main__":
    sys.exit(main())
