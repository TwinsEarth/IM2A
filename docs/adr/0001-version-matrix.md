# ADR 0001: Platform v2.3.45 / Go v2.3.26

## Status

Accepted (2026-10-29).

## Decision

- Platform release tag: `v2.3.45`
- Go gateway tag: `v2.3.26`
- The platform version is not forced onto every component.
- Component and protocol versions are listed in `version-matrix.json` and emitted into `release-manifest.json`.

## Consequences

- A platform release can ship a Go patch without renumbering every package.
- `release-manifest.json` is the only release-time source of version evidence.
- No component may be silently downgraded to make the numbers match.
