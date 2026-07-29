# RFP: splunk-mcp

> Generated: 2026-07-30
> Status: Draft

## 1. Problem Statement

Splunk's official MCP Server app (installed on the Splunk side) executes searches with `exec_mode=oneshot` under a 60-second HTTP timeout and silently injects `| head N` into the SPL (default cap 1000 rows). As a result, row counts are unstable, and when the cap is hit only an approximation (`approx_total: "1000+"`) is returned. There is structurally no way to obtain an exact total or the full result set, making it unusable for data analysis — and unstable results cause agents to loop on retries.

splunk-mcp is a local MCP server that calls the Splunk REST API directly using the asynchronous job pattern, **guaranteeing exact counts and full-result retrieval**. Target user: myself, performing Splunk data analysis from Claude Code / MCP clients.

## 2. Functional Specification

### Commands / API Surface

MCP tools (Phase 1 core):

| Tool | Behavior |
|---|---|
| `run_query` | Execute SPL. Internally creates a job with `exec_mode=normal`, polls until `dispatchState=DONE`, then returns the exact `resultCount` and results. Never uses oneshot or results_preview |
| `start_query` | Start a job and return the SID immediately (for long-running searches) |
| `check_job` | Check dispatchState / progress / final counts |
| `get_results` | Fetch results from a completed job (full retrieval via offset/count pagination) |
| `cancel_job` | Cancel a job |
| `get_usage` | Self-documentation (nlink-jp MCP standard) |

Discovery tools added in Phase 2:

| Tool | Behavior |
|---|---|
| `list_indexes` | List indexes (with event counts and time ranges) |
| `list_sourcetypes` | List sourcetypes (optionally scoped to an index) |
| `list_saved_searches` | List saved searches |
| `run_saved_search` | Run a saved search (result delivery shares run_query's mechanism) |

### Input / Output

- Input: SPL string, time range (earliest / latest, Splunk time modifier format), app context, row threshold, workspace_root
- Output (two delivery modes):
  - Results at or below the threshold (default 100 rows, adjustable via config / tool argument) → inline JSON
  - Above the threshold → **all** rows written as JSONL under `workspace_root`; returns the `results_file` path + head preview + exact count (no truncation; the file can be fed directly into data-toolbox-mcp for further analysis)
- Tool errors are structured JSON (`{code, message}`, nlink-jp MCP convention)

### Configuration

**One MCP instance = one Splunk connection.** No profile mechanism; use one config file per destination and register the MCP server multiple times under different names.

```toml
# e.g. ~/.config/splunk-mcp/prod.toml
[splunk]
host  = "https://splunk-prod.example.com:8089"
token = "..."
# app = "search"
# insecure = false
# http_timeout = "30s"
# prepend = "pipe-only"   # auto | pipe-only | off (same 3 modes as splunk-cli)

[server]
inline_row_threshold = 100
# job_ttl = "10m"
```

```json
// MCP client registration example
{
  "splunk-prod": { "command": "splunk-mcp", "args": ["--config", "~/.config/splunk-mcp/prod.toml"] },
  "splunk-dev":  { "command": "splunk-mcp", "args": ["--config", "~/.config/splunk-mcp/dev.toml"] }
}
```

- Config path via `--config` flag or env var (default `~/.config/splunk-mcp/config.toml`)
- Precedence identical to splunk-cli: flags → env vars (`SPLUNK_HOST` / `SPLUNK_TOKEN`, etc.) → config file
- Config file permission warning at 600 (same behavior as splunk-cli)

### External Dependencies

- Splunk Enterprise / Splunk Cloud REST API (management port 8089)
- Auth: Bearer token (recommended) or Basic auth
- **No app installation on the Splunk side** (a key operational advantage over the official MCP app)

## 3. Design Decisions

- **Language: Go** — reuses splunk-cli assets (REST client, auth, prepend normalization) and rides the same build/signing/release infrastructure as existing nlink-jp MCP servers
- **Skeleton: ported from data-toolbox-mcp** — the standard procedure for new MCP servers (get_usage, structured errors, file-mediated pattern)
- **Code sharing with splunk-cli: copy-port** — copy splunk-cli's internal packages into splunk-mcp and maintain independently. Immediately actionable, independent release cadence. The REST layer is stable, so drift risk is low (a shared library was rejected: synchronized releases across three repos are not worth the cost)
- **Multi-instance: config-path switching** — one instance = one destination, distinguished by MCP registration name. No profile mechanism or `profile` tool argument (keeps the tool surface simple and makes SID-to-instance mapping self-evident)
- **Safety guard: block destructive commands only** — write/delete commands such as `| delete`, `| collect`, `| outputlookup` are rejected by default, individually allowable via config. Read/analysis commands are unrestricted. The official app's safe_spl allowlist is a multi-tenant design whose maintenance cost is unjustified for single-user use. The last line of defense is Splunk-side RBAC (the token's role permissions)
- **Explicitly out of scope**: SPL2 support (the official app's SPL2→SPL1 compilation is unnecessary), OAuth / SSO, multi-tenant guardrails (rate limits, tool roles, etc.), real-time searches, distribution as a Splunk-side app

Complementary relationships: splunk-cli (human, hands-on) and splunk-mcp (agent-driven) form a pair sharing the same REST-layer design. Large results connect to data-toolbox-mcp workspaces for continued analysis.

## 4. Development Plan

### Phase 1: Core

- Port the MCP skeleton from data-toolbox-mcp; copy-port the REST client / auth / prepend normalization from splunk-cli
- Six core tools (run_query / start_query / check_job / get_results / cancel_job / get_usage)
- Asynchronous job pattern (create job → poll → exact count → paginated full retrieval)
- File-mediated delivery (threshold, JSONL dump, preview)
- Destructive-command guard
- Tests: mock HTTP server validating the REST layer, job lifecycle, guard, and threshold branching

### Phase 2: Features

- Four discovery tools (list_indexes / list_sourcetypes / list_saved_searches / run_saved_search)
- Reviewable independently of Phase 1

### Phase 3: Release

- docs/{en,ja}, README.md / README.ja.md / CHANGELOG.md / AGENTS.md
- Real-data E2E against a live Splunk instance (mandatory before release)
- Signing + notarization, 12-step release process, umbrella submodule update, check-org.sh

## 5. Required API Scopes / Permissions

- Splunk Bearer token (or Basic auth) only
- Required permissions: search capability + read access to target indexes. RBAC fully follows the role bound to the token
- OAuth scopes / IAM roles: None

## 6. Series Placement

Series: **util-series**

Reason: nlink-jp MCP servers (data-toolbox-mcp, voice-studio-mcp, pcap-analyzer-mcp, etc.) are consolidated in util-series; follow this de facto rule. cli-series is defined as "Interactive CLI clients," which excludes MCP servers (splunk-mcp pairs with splunk-cli but does not live beside it).

## 7. External Platform Constraints

- REST API via management port 8089. Self-signed certificates are common, so the `insecure` option is inherited
- Search job TTL: jobs are auto-deleted minutes after completion by default. `get_results` must be called within the TTL; extendable via the `ttl` parameter at job creation
- Server-side `limits.conf` constraints: per-request result row caps exist, so full retrieval is absorbed by offset/count pagination
- Per-role concurrent search quotas: many parallel agent jobs can exhaust them (noted in get_usage)
- Splunk Cloud requires IP allowlisting for management-port access
- The job API uses v1 endpoints (`services/search/jobs`) (**revised during implementation**: originally drafted as v2, but unified on v1, which splunk-cli has proven against real Splunk; v2 job status/control support is version-dependent, and v1 removes the Splunk 9.x-only constraint)

---

## Discussion Log

- **Background**: Inspected the source of the official Splunk MCP Server app (`oss/Splunk_MCP_Server`) and confirmed as implementation fact: oneshot execution (60 s timeout) + `| head N` injection (max_row_limit 1000) + approximate counts when the cap is hit. The unstable counts are by design, not a bug, and cannot be fixed externally — hence the decision to fully rebuild as a local MCP server calling the REST API directly
- **Tool name**: splunk-mcp (shortest name pairing with splunk-cli; survives scope expansion). splunk-search-mcp / splunk-query-mcp rejected
- **Scope**: full configuration — query-execution core plus metadata discovery and saved-search execution (discovery split into Phase 2)
- **Safety guard**: "block destructive only" adopted. "No guard (rely on RBAC)" and "read-only allowlist" rejected (the latter inherits the official app's allowlist maintenance-cost problem)
- **Result delivery**: threshold branching (inline / file-mediated) adopted. "Always write to file" rejected due to overhead on small results
- **Job API version**: changed from v2 (`search/v2/jobs`) to v1 (`services/search/jobs`) during Phase 1 implementation, preferring the paths splunk-cli has proven in production and removing the Splunk 9.x-only constraint
- **Multi-instance**: a profile design (`[profiles.<name>]` + `profile` tool argument) was initially proposed, but the user decided on **config-path switching (one MCP instance = one destination)** — destinations distinguished by MCP registration name, keeping the tool surface simple
