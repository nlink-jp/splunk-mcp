# AGENTS.md — splunk-mcp

## Project summary

Local MCP server exposing Splunk search over the REST API. Replaces Splunk's
official MCP Server app, whose oneshot + 60 s timeout + `| head` injection
makes row counts unstable. Every search runs as an asynchronous Splunk job
(create → poll `DONE` → exact `resultCount` → paged retrieval), so counts
are always exact. Rows come back in the response under an explicit `max_rows`
cap, and what the cap drops is counted (`truncated`, `omitted_rows`) rather
than quietly cut — this server writes no result files (ADR-0004).

One server instance = one Splunk host (config-path switching, no profile
mechanism). RFP: `docs/ja/splunk-mcp-rfp.ja.md`.

## Build & test

```bash
make build             # → dist/splunk-mcp  (never `go build` directly — pollutes repo root)
make test              # go test ./...  (unit; mock Splunk, no container)
make test-linux        # same suite on Linux (container)
make vet               # go vet ./...
make check             # vet + test + build
make integration-test  # Podman splunk/splunk:9.4 container + `-tags integration` live E2E
make splunk-down       # tear down the container (name `splunk-test`, shared with splunk-cli)
make verify-release  # gate: .notarized marker + freshness (run before upload)
```

`--version` must keep answering (homebrew formula test depends on it);
version is injected via `-ldflags -X github.com/nlink-jp/splunk-mcp/cmd.Version`.

## Structure

```
main.go                    Entry point — calls cmd.Execute()
cmd/
  root.go                  Cobra root; default action = serve; --config flag
  serve.go                 Config resolution, wiring, stdio serve loop
  version.go               Version subcommand + --version flag
internal/
  transport/               Newline-delimited JSON-RPC stdio (1MB lines)   [ported: data-toolbox-mcp]
  jsonrpc/                 JSON-RPC 2.0 types + codes                     [ported: data-toolbox-mcp]
  mcpserver/               MCP 2024-11-05 routing, RegisterTool, RawResult [ported: data-toolbox-mcp]
  toolerr/                 Structured {code,message,details} tool errors  [ported: data-toolbox-mcp, codes swapped]
  logging/                 slog setup + startup log rotation              [ported: data-toolbox-mcp]
  config/                  TOML [splunk]+[server] config, env overrides   [ported: splunk-cli, extended]
  spl/                     prepend modes (auto|pipe-only|off) + destructive-command guard
  client/                  Splunk REST client (jobs v1 endpoints)         [ported: splunk-cli, slog + TTL + raw rows]
                           + discovery.go (indexes / saved-search listing, dispatch)
  tools/                   The 10 MCP tools + the max_rows cap and its accounting
config.example.toml        Template config (one file per Splunk host)
```

## Key decisions & gotchas

- **v1 job endpoints** (`services/search/jobs`), not `search/v2/...` —
  proven by splunk-cli against real Splunk; no 9.x-only constraint.
  (Deviation from the RFP draft, recorded in RFP §7.)
- **Never write to stdout** except JSON-RPC responses — logs go to stderr /
  log file via slog. Ported client code was changed from fmt.Fprintf(stderr)
  to slog for this reason.
- **workspace_required is checked before fetching** so a huge result is
  never downloaded just to be thrown away; the error carries `sid` +
  `total_rows` and the job stays alive for a retry.
- **run_query wait_timeout does NOT cancel the job** — the SID in the error
  details lets the agent continue with check_job / get_results.
- The guard splits on `|` without quote-parsing — quoted text containing
  `| delete` false-positives (safe side, documented in guard_test).
- `FetchResults` advances by `len(page)`, not the requested page size, so a
  server-side `limits.conf` cap smaller than our 50k page still terminates.
- Tests use `Client.PollInterval` (default 2 s) shortened to ms; fakeSplunk
  in `internal/tools/integration_test.go` counts status polls — each
  checkJob/getResults call consumes one.
- Live E2E (`client_integration_test.go`, `tools_integration_test.go`,
  `//go:build integration`) skips unless SPLUNK_HOST/SPLUNK_TOKEN are set;
  `make integration-test` provisions them from the container. Harness ported
  from splunk-cli (`scripts/splunk-up.sh` / `splunk-down.sh`); details in
  BUILD.md.
- MCP has no protocol-level cancel: request handling is serial, so a long
  run_query blocks the loop. get_usage steers agents to start_query for
  anything slow.
- list_sourcetypes interpolates the index arg into SPL — it is validated
  against `^[A-Za-z0-9_*-]+$` so injection is impossible; widen with care.
- run_saved_search always sends `trigger_actions=0` (an analysis tool must
  never fire alert actions) and escapes the name with url.PathEscape —
  url.JoinPath would treat an embedded "/" as a path separator.
- **Every tool schema sets `additionalProperties: false`** (organization
  ADR-021 §10). The schemas here are ten separate JSON literals with no
  shared builder, so a new tool must add the key by hand —
  `TestEveryToolSchemaIsClosed` (`internal/tools/schema_test.go`) is what
  catches the omission, and it reads the schemas off a real `tools/list`
  driven through `Register`, not from the literals.
- **The server side enforces it too.** `parseArgs` decodes with
  `json.Decoder.DisallowUnknownFields`, and every handler goes through it —
  including `list_indexes`, `list_saved_searches` and `get_usage`, which take
  no arguments, because "none" means none rather than any. **A call carrying
  an argument a tool does not declare now fails, naming the field, instead of
  being silently ignored** — the deliberate behaviour change ADR-021 §4
  requires, made because the closed schemas bind validating clients only and a
  caller speaking JSON-RPC directly validates nothing. A misspelled `max_rows`
  used to fall back to the config default while looking like it took effect,
  which undercut the exact-count guarantee this server exists for.
  `TestEveryToolRefusesAnUnknownArgument` replaced
  `TestParseArgsAcceptsUnknownFields`, which had pinned the opposite. Do not
  add a compatibility shim: an argument name no tool declares has never meant
  anything.
- **Every handler decodes before it touches Splunk, and a test depends on it.**
  `callTool` in `schema_test.go` drives real `tools/call` requests through a
  server registered with a **nil** client, so a call that must be refused has
  to come back as an error without reaching Splunk. If that ordering is ever
  broken the nil dereference panics, which is the intent — put the
  `parseArgs` call first in a new handler.
- **`contract_test.go`'s `allTools()` is a hand-written copy of the registry.**
  A tool registered in `tools.go` but missing from that list is exempt from
  every assertion in `contract_test.go` with nothing failing.
  `TestContractToolListMatchesTheRegistry` now pins the two together in both
  directions; prefer deriving from `registeredTools` for any new list-walking
  test rather than extending `allTools()`.

## Release

Follow the org 12-step release process (CONVENTIONS.md). Before release:
real-data E2E against a live Splunk instance is mandatory (RFP Phase 3).
