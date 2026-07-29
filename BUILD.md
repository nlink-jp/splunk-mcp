# Development Guide

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.24+ | Build and test |
| Podman | 4.0+ | Integration tests only |
| Python 3 | any | `scripts/splunk-up.sh` helper |
| curl | any | `scripts/splunk-up.sh` helper |

## Build

```bash
make build       # current platform → dist/splunk-mcp
make build-all   # cross-compile → dist/
make clean       # remove artifacts
```

Never run `go build` directly — it drops the binary in the project root.

## Unit tests

```bash
make test        # go test ./...  (no external dependencies)
make check       # vet + test + build
```

Unit tests cover the REST client, config, SPL prepend/guard, the MCP server
skeleton, and the tool layer — the tool tests run against an in-process mock
Splunk (`internal/tools/integration_test.go`), so no container is needed.

## Integration tests (live Splunk container)

Integration tests carry `//go:build integration` and run against a real
Splunk started in a Podman container
(`docker.io/splunk/splunk:9.4`, platform `linux/amd64` — emulated on
Apple Silicon, which is slow to boot but works).

```bash
make integration-test   # starts the container if needed, runs the tests
make splunk-down        # tear the container down when done
```

What happens:

1. `scripts/splunk-up.sh` starts (or reuses) a container named
   `splunk-test`, maps 8089 to a random local port in 18000–18999, waits up
   to 300 s for the REST API, and obtains an admin session token.
   The container name is shared with splunk-cli's harness, so a container
   started by either project is reused by the other.
2. `make integration-test` resolves the port and token, then runs
   `go test -tags integration ./internal/client/... ./internal/tools/...`.

The tests skip (not fail) when `SPLUNK_HOST` / `SPLUNK_TOKEN` are unset, so
plain `make test` never touches the network.

To run against an arbitrary Splunk instead of the container:

```bash
SPLUNK_HOST="https://your-splunk:8089" SPLUNK_TOKEN="..." \
  go test -v -tags integration ./internal/client/... ./internal/tools/...
```

Coverage highlights on the tools side (`tools_integration_test.go`):

- `TestLive_RunQuery_ExactCountInline` — the exact-count guarantee end to end
- `TestLive_RunQuery_FileMediated` — every generated row lands in the JSONL
  file (no truncation)
- `TestLive_RunQuery_WorkspaceRequired` — pre-fetch guard, then paging the
  same SID via `get_results`
- `TestLive_AsyncFlow` — `start_query` → `check_job` → `get_results`
- `TestLive_CancelJob`, `TestLive_JobFailed`

## Manual stdio smoke test

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | SPLUNK_HOST="https://localhost:8089" SPLUNK_TOKEN=dummy ./dist/splunk-mcp
```
