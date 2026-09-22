# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).
- **The Linux archives no longer carry macOS file metadata.** macOS `tar` wrote
  each bundled file's extended attributes (`com.apple.provenance`, and a Dropbox
  attribute where the tree is synced) into the `.tar.gz` twice: as AppleDouble
  `._` members, which GNU tar extracts as stray `._<name>` files beside the real
  ones, and as `LIBARCHIVE.xattr.*` / `SCHILY.xattr.*` pax headers, which it
  reports as unknown keywords. `make package` now archives with
  `COPYFILE_DISABLE=1 tar --no-xattrs`; each setting stops one of the two.
  Archives already published still carry them; the files themselves are
  unaffected.

### Internal

- `make verify-release` also judges each Linux archive: no AppleDouble or other
  macOS metadata members — listed with `--options 'tar:!mac-ext'`, because a
  plain macOS listing folds `._` members away — no extended attributes as pax
  headers, and exactly the canonical binary, `README.md` and `LICENSE`, compared
  in the C locale.

## [0.3.0] - 2026-09-21

### Changed

- **An MCP tool call carrying an argument the tool does not declare now fails
  instead of being quietly ignored.** This is a deliberate behaviour change,
  required by org ADR-021 §4, and it is the one the previous entry recorded as
  still outstanding. `max_rows` is what made it costly: until now `max_rowz`
  was accepted and dropped, so the call fell back to the configured default
  while reading as though the caller's cap had been honoured — and an exact,
  explicit row count is the guarantee this server exists to give. All ten
  tools — including `list_indexes`, `list_saved_searches` and `get_usage`,
  which take no arguments — now decode with `DisallowUnknownFields` and refuse
  the call, naming the offending field:
  `{"code":"invalid_arguments","message":"invalid arguments: json: unknown field \"max_rowz\""}`.

  A malformed argument object is refused the same way, rather than leaving the
  argument at its zero value and running as though it had been absent.

  Nothing runs before the arguments decode, so a rejected call reaches no
  Splunk endpoint and starts no search job. Omitting `arguments`, or sending
  `{}` or `null`, still means "no arguments" and is not an error. There is no
  compatibility shim: an argument name no tool declares has never meant
  anything, so the only fix is to correct it.

### Fixed

- All ten MCP tool schemas now set `additionalProperties: false`, as
  organization ADR-021 §10 requires, so a client validating arguments against
  the schema refuses a mistyped parameter instead of forwarding it. The
  schemas are separate JSON literals with no shared builder, so the key was
  added to each; `TestEveryToolSchemaIsClosed` catches the next omission and
  reads the schemas off a real `tools/list` driven through `Register`.

### Added

- `TestContractToolListMatchesTheRegistry` — `contract_test.go` asserts schema
  shape over a hand-written `allTools()`, and a tool registered in `tools.go`
  but missing from that list was exempt from every assertion there with
  nothing failing. The two are now pinned together in both directions.
- `TestEveryToolRefusesAnUnknownArgument` — walks the tool list from the
  registry (not the hand-written `allTools()`) and asserts each tool refuses
  an undeclared argument and names it. It replaces
  `TestParseArgsAcceptsUnknownFields`, which pinned the opposite behaviour and
  said it should be inverted when this change landed.
- `TestMisspelledMaxRowsIsRefused` and `TestMalformedArgumentsAreRefused` —
  the specific regression (`max_rowz` silently using the default) and the
  wrong-typed-argument half, both driven through real `tools/call` requests.

## [0.2.2] - 2026-09-14

### Added

- `TestEveryRequiredNameIsDeclared` — a schema that lists a name in `required`
  without declaring it in `properties` makes a strict client refuse the whole
  tool list (Vertex AI: "schema at top-level requires unspecified property").
  data-toolbox-mcp shipped exactly that and broke a session outright; the
  existing contract test checked declared ⇒ required only, so the fleet is
  pinned in both directions now.

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
