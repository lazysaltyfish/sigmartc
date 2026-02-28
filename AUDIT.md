# Project Audit (sigmartc)

## Summary
The codebase is a small Go + vanilla JS SFU voice chat with clear separation of
backend and frontend. Deployment scripts and Docker setup exist, but the repo
needed hygiene work (runtime artifacts, missing gitignore, and missing user docs).

## Key Findings

### Repo Hygiene
- Runtime artifacts were present in the tree (binary, logs, tooling drafts).
- No git repository or gitignore existed prior to cleanup.
- The Docker build relies on a bundled Go toolchain tarball.

### Runtime and Operations
- Logs are emitted via `slog` and are buffered for admin viewing; file logging
  is now supported (see `internal/logger/logger.go`).
- The admin panel exposes stats/logs/ban actions under `/admin`.
- `banned_ips.json` persists bans to disk; missing file is tolerated.

### Networking and Security
- `X-Forwarded-For` is trusted directly. This is safe only behind a trusted
  reverse proxy that overwrites/cleans the header.
- STUN is configured by default; TURN relay is supported via command-line flags.

### Test Coverage
- No automated tests. The manual verification checklist is the primary validation.

## Recommendations

### Short Term
- Keep runtime artifacts out of git (`bin/`, logs, ban list).
- Ensure the reverse proxy sanitizes `X-Forwarded-For`.
- Keep docs synchronized with behavior (AGENTS/README/DEPLOY).

### Medium Term
- Consider replacing the bundled Go tarball with an official builder image to
  reduce repo size.
- Add a small smoke-test script for CI (HTTP + WS checks).

## Notes
- The StreamID mapping (sender PeerID) is critical for frontend UI mapping.
