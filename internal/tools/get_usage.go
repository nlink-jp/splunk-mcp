package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
)

var getUsageTool = mcpserver.Tool{
	Name:        "get_usage",
	Description: "Full tool reference for splunk-mcp: workflow, result-delivery contract, and error-recovery table. Call this before your first query.",
	InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
}

func (d *deps) getUsage(ctx context.Context, args json.RawMessage) (any, error) {
	host := d.cfg.Host
	if host == "" {
		host = "(not configured — set [splunk] host or SPLUNK_HOST)"
	}
	allow := "(none)"
	if len(d.cfg.AllowCommands) > 0 {
		allow = strings.Join(d.cfg.AllowCommands, ", ")
	}
	text := fmt.Sprintf(usageTemplate, host, d.cfg.InlineRowThreshold, previewRowCount, allow)
	return mcpserver.RawResult{
		Content: []mcpserver.ContentBlock{{Type: "text", Text: text}},
	}, nil
}

// usageTemplate is the get_usage document. Verb placeholders: host,
// inline_row_threshold, preview row count, allow_commands.
const usageTemplate = `# splunk-mcp usage

Local MCP server for Splunk data analysis over the REST API. One server
instance connects to exactly one Splunk host — this instance talks to:
%s

Every search runs as an asynchronous Splunk job (never oneshot / preview
endpoints), so **total_rows is always the exact final count** and full
retrieval is guaranteed. There is no silent truncation.

## Tools

| Tool | Purpose |
|---|---|
| run_query | Run SPL, wait for completion, return exact count + results |
| start_query | Start SPL asynchronously, return SID immediately |
| check_job | Poll job state / result count by SID |
| get_results | Fetch results of a completed job (offset/count paging) |
| cancel_job | Cancel a running job |
| get_usage | This document |

## Typical workflows

Quick analysis (completes within wait_seconds, default 300):
1. run_query {"spl": "index=main sourcetype=syslog | stats count by host"}

Long-running search:
1. start_query {"spl": "..."} -> sid
2. check_job {"sid": "..."} until is_done
3. get_results {"sid": "..."} (page with offset/count, or pass workspace_root)

Large result sets (file mediation):
- Results with more rows than the inline threshold (currently %d) are NOT
  truncated — pass workspace_root (absolute path) and the full set is written
  as a JSONL file (one JSON object per line). The response carries
  results_file, a %d-row head preview, and the exact total_rows.
- The JSONL file can be loaded directly into data-toolbox-mcp for further
  analysis (load_data with format jsonl).

## Guard

Write/delete SPL commands (delete, collect, mcollect, meventcollect,
outputlookup, outputcsv, sendemail, runshellscript, script) are rejected with
code "unsafe_spl". Currently allowed via config: %s.
Read/analysis commands are unrestricted; Splunk RBAC remains the final
authority.

## Error recovery

| code | Meaning | Recovery |
|---|---|---|
| missing_argument / invalid_arguments | Bad tool input | Fix the arguments |
| unsafe_spl | Blocked write/delete command | Rework the SPL; allow_commands in config if truly intended |
| wait_timeout | run_query hit wait_seconds; job still running | Poll check_job with the returned sid; get_results when done; or cancel_job |
| job_not_done | get_results on an unfinished job | Poll check_job until is_done |
| job_not_found | SID expired (TTL) or wrong | Re-run the search; consider [server] job_ttl in config |
| job_failed | Splunk reported FAILED | Read error_messages in details; fix the SPL |
| workspace_required | Result exceeds inline threshold, no workspace_root | Retry with workspace_root (absolute path), raise inline_row_threshold, or page with get_results offset/count |
| workspace_error | Could not write results file | Check the workspace_root path/permissions |
| splunk_api_error | HTTP/auth/network failure | Check host, token validity, and network; details are in the message |

## Constraints worth knowing

- Completed jobs expire after their TTL (Splunk default is a few minutes;
  raise via [server] job_ttl). get_results after expiry returns job_not_found.
- Splunk enforces per-role concurrent-search quotas; many parallel jobs can
  exhaust them. Prefer sequential start_query batches.
- run_query blocks this server until done or wait_seconds; for anything
  potentially slow, use start_query so other tool calls stay responsive.
`
