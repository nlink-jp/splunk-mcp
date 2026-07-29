# splunk-mcp

A local MCP server that exposes Splunk search over the REST API — built for
data analysis where **exact result counts and full retrieval are
non-negotiable**.

[日本語版 README はこちら](README.ja.md)

## Why

Splunk's official MCP Server app (installed on the Splunk side) runs searches
with `exec_mode=oneshot` under a 60-second timeout and silently injects
`| head N` (default cap 1000 rows). Row counts are unstable, capped results
report only an approximation, and there is no way to retrieve the full set —
unusable for data analysis, and a trigger for agent retry loops.

splunk-mcp runs every search as an **asynchronous Splunk job** (create →
poll until `DONE` → read the final `resultCount` → page through
`/results`), so:

- `total_rows` is always the exact final count — never a preview, never an
  approximation
- Large result sets are **never truncated**: they are written as a JSONL
  file under a caller-supplied `workspace_root`, with a head preview inline
- Long searches don't time out: `run_query` returns a SID on `wait_timeout`
  and the job keeps running server-side
- No app installation on the Splunk side — a token is all you need

## Tools

| Tool | Purpose |
|---|---|
| `run_query` | Run SPL, wait for completion, return exact count + results |
| `start_query` | Start SPL asynchronously, return the SID immediately |
| `check_job` | Poll job state / result count by SID |
| `get_results` | Fetch results of a completed job (offset/count paging) |
| `cancel_job` | Cancel a running job |
| `get_usage` | Full tool reference served to the agent |

### Result delivery

Results at or below the inline threshold (default 100 rows) are returned
inline as JSON. Above it, **all** rows are written as a JSONL file (one JSON
object per line) under `workspace_root`, and the response carries the file
path, a 5-row preview, and the exact `total_rows`. The file loads directly
into [data-toolbox-mcp](https://github.com/nlink-jp/data-toolbox-mcp) for
further analysis.

### SPL guard

Write/delete commands (`delete`, `collect`, `mcollect`, `meventcollect`,
`outputlookup`, `outputcsv`, `sendemail`, `runshellscript`, `script`) are
rejected by default with a structured `unsafe_spl` error. Individual commands
can be re-allowed via `[server] allow_commands`. Splunk-side RBAC remains the
final authority.

## Installation

Download a pre-built binary from the releases page, or build from source:

```bash
git clone https://github.com/nlink-jp/splunk-mcp.git
cd splunk-mcp
make build
# Binary: dist/splunk-mcp
```

## Configuration

**One server instance connects to exactly one Splunk host.** For multiple
destinations, create one config file per host and register the server
multiple times:

```json
{
  "mcpServers": {
    "splunk-prod": { "command": "splunk-mcp", "args": ["--config", "/path/to/prod.toml"] },
    "splunk-dev":  { "command": "splunk-mcp", "args": ["--config", "/path/to/dev.toml"] }
  }
}
```

Copy [config.example.toml](config.example.toml) to
`~/.config/splunk-mcp/config.toml` (the default path) and set your values:

```toml
[splunk]
host  = "https://your-splunk.example.com:8089"
token = "your-token"
# insecure = false          # self-signed certs
# prepend  = "pipe-only"    # auto | pipe-only | off (same as splunk-cli)

[server]
# inline_row_threshold = 100
# job_ttl              = "10m"
# allow_commands       = []
```

```bash
chmod 600 ~/.config/splunk-mcp/config.toml
```

Config resolution order: `--config` flag → `$SPLUNK_MCP_CONFIG` →
`~/.config/splunk-mcp/config.toml` → `./config.toml`. Connection settings in
the file are overridden by env vars (`SPLUNK_HOST`, `SPLUNK_TOKEN`,
`SPLUNK_USER`, `SPLUNK_PASSWORD`, `SPLUNK_APP`) — the same names splunk-cli
uses, so credentials can be shared.

## Usage

```bash
splunk-mcp                    # serve MCP over stdio (default)
splunk-mcp serve --config /path/to/prod.toml
splunk-mcp --version
```

Typical agent workflows:

- **Quick analysis** — `run_query` with SPL; completes within `wait_seconds`
  (default 300) and returns exact counts.
- **Long-running search** — `start_query` → poll `check_job` → `get_results`.
- **Large result set** — pass `workspace_root` (absolute path); the full set
  arrives as a JSONL file plus preview. No rows are ever dropped.

## Operational notes

- Completed jobs expire after their TTL (Splunk default is a few minutes;
  raise with `[server] job_ttl`). An expired SID returns `job_not_found`.
- Splunk enforces per-role concurrent-search quotas; prefer sequential
  `start_query` batches over mass parallelism.
- Tool errors are structured JSON `{code, message, details}` — see
  `get_usage` for the full error-recovery table.

## Development

```bash
make test              # go test ./...  (unit tests, no external deps)
make vet               # go vet ./...
make check             # vet + test + build
make build             # outputs dist/splunk-mcp
make integration-test  # start a Splunk container (Podman) and run live E2E tests
make splunk-down       # stop and remove the Splunk test container
```

Integration tests run the full lifecycle — exact counts, JSONL file
mediation, async flow — against a real `splunk/splunk:9.4` container.
See [BUILD.md](BUILD.md) for details.

## License

MIT — see [LICENSE](LICENSE).
