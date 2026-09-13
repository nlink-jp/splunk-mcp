# Changelog

All notable changes to this project will be documented in this file.

## [0.2.1] - 2026-09-13

### Fixed

- **The tool descriptions and the `get_usage` manual still described the result
  file that 0.2.0 removed.** `run_query` told the model large results are
  written as JSONL under `workspace_root`; `get_usage` listed
  `workspace_required` / `workspace_error` and advised retrying with
  `workspace_root` or raising `inline_row_threshold` — all of which this server
  now rejects. The same text in `get_results`, `run_saved_search`, the package
  doc, `--help`, README and AGENTS is corrected too. What the model is told is
  now what the server does: rows come back capped by `max_rows`, and the drop is
  counted.
- The `workspace_required` and `workspace_error` error codes are gone; nothing
  could return them.
- The integration suite (`-tags integration`) did not compile after 0.2.0 —
  it still referenced `results_file` and the head preview. Its two
  file-mediation tests are replaced by `TestLive_RunQuery_MaxRowsCap` (the cap
  reports what it dropped, `total_rows` stays exact) and
  `TestLive_RunQuery_CapThenPage` (a capped result leaves the job fetchable).

### Added

- `TestModelFacingTextNamesNoRetiredMechanism` — walks every registered tool's
  description and schema plus the usage manual and fails on a retired term.
  Prose drifts silently because nothing compiles it; this test compiles it.
- `make test` now also runs `go vet -tags integration ./...`. `go test ./...`
  never builds tagged files, which is how a broken live suite went unnoticed.

## [0.2.0] - 2026-09-13

### Changed

- **Breaking: results are no longer written to a file by this server.**
  `workspace_root`, `results_file` and the head preview are gone, and so is the
  inline/file branching. A server cannot know the caller's context window;
  deciding a threshold, a destination and a read-back path per server meant the
  whole fleet reimplementing the same mechanism, and putting a large response on
  disk is the calling runtime's job (gem-agent does it automatically). What
  stays here is the explicit cap and the count of what it dropped.
- **Breaking: `inline_row_threshold` is replaced by `max_rows`** — as a config
  key and as a per-call argument, with a different meaning: the cap on rows a
  response may carry, default **50,000**, `0` meaning no cap. A config still
  carrying `inline_row_threshold` fails at startup rather than being ignored,
  because a limit an operator wrote and had ignored is the worse outcome.
- Results that hit the cap carry `truncated`, `omitted_rows` and a note saying
  how to get the rest. **`total_rows` stays exact either way** — the count is
  the product, so a capped answer is still an answer about the whole result set.
- The `workspace_required` error code is gone with the mechanism; nothing
  replaces it (`max_rows` never fails, it reports).

## [v0.1.0] - 2026-07-30

Initial release.

### Added

- Discovery tools (RFP Phase 2): `list_indexes` (REST entity listing with
  event counts and time bounds), `list_sourcetypes` (`| metadata` search
  with strict index-name validation), `list_saved_searches`, and
  `run_saved_search` (dispatch under the run_query contract;
  `trigger_actions=0` always, `saved_search_not_found` structured error,
  names escaped so spaces and slashes are safe). Live container tests cover
  all four.
- Initial implementation (RFP Phase 1: Core).
- MCP stdio server skeleton ported from data-toolbox-mcp
  (transport / jsonrpc / mcpserver / toolerr / logging).
- Splunk REST layer ported from splunk-cli (client / config / spl prepend),
  adapted for MCP: slog logging, raw result rows, job TTL support
  (`[server] job_ttl` sent as the job's keep-alive timeout).
- Core tools: `run_query`, `start_query`, `check_job`, `get_results`,
  `cancel_job`, `get_usage`.
- Asynchronous job pattern: create job → poll until `DONE` → exact
  `resultCount` → paged full retrieval. No oneshot / preview endpoints.
- Result-delivery contract: inline JSON at or below `inline_row_threshold`
  (default 100 rows); above it, full JSONL file under `workspace_root` with
  a 5-row preview and the exact total. Never truncates.
- Destructive-SPL guard: `delete`, `collect`, `mcollect`, `meventcollect`,
  `outputlookup`, `outputcsv`, `sendemail`, `runshellscript`, `script`
  blocked by default; per-command opt-out via `[server] allow_commands`.
- Structured tool errors `{code, message, details}` with recovery guidance
  in `get_usage`.
- Config: one instance = one Splunk host; `--config` /
  `$SPLUNK_MCP_CONFIG` path switching; `SPLUNK_*` env vars shared with
  splunk-cli.
- Container-based integration tests (`make integration-test`): Podman
  `splunk/splunk:9.4` harness ported from splunk-cli, plus tool-layer live
  E2E covering the exact-count guarantee, JSONL file mediation, async flow,
  cancel, and failure surfacing (BUILD.md documents the workflow).

[v0.1.0]: https://github.com/nlink-jp/splunk-mcp/releases/tag/v0.1.0
