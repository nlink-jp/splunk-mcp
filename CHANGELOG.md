# Changelog

All notable changes to this project will be documented in this file.

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
