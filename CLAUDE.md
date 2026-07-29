# splunk-mcp — CLAUDE.md

Project-specific instructions for Claude Code.
Org conventions: https://github.com/nlink-jp/.github/blob/main/CONVENTIONS.md
Project summary, structure, and gotchas: see `AGENTS.md`.

## Non-negotiable rules

- **Tests are mandatory** — a feature is not complete without tests.
- **Never `go build` directly** — always `make build` (outputs to `dist/`).
- **Docs in sync** — README.md and README.ja.md updated in the same commit
  as behaviour changes.
- **Small, typed commits** — `feat:`, `fix:`, `test:`, `chore:`, `docs:`.

## Project-specific rules

- **stdout is the JSON-RPC channel.** Nothing else may ever write to it —
  no fmt.Println, no library that prints. Diagnostics go through slog
  (stderr / log file).
- **The exact-count guarantee is the product.** Any change that could
  truncate results, return preview counts, or silently cap a fetch is a
  design regression — file-mediate instead.
- **Error responses are structured** (`internal/toolerr`): every new failure
  path returns a stable code and, where an agent must act on it (SID to
  poll, path to fix), machine-readable details.
- New tools follow the existing shape: `var xxxTool = mcpserver.Tool{...}`
  descriptor + `(d *deps) xxx(ctx, args)` handler + registration in
  `tools.go` + coverage in `integration_test.go` + get_usage table row.
- The RFP (`docs/ja/splunk-mcp-rfp.ja.md`) records scope decisions —
  update it when a decision changes (as done for v1-vs-v2 endpoints).
