package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var getResultsTool = mcpserver.Tool{
	Name: "get_results",
	Description: "Fetch results of a completed job. Defaults to all rows; use offset/count to page. " +
		"The same inline-vs-file contract as run_query applies to the fetched slice.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"sid": {"type": "string", "description": "Search job SID."},
			"offset": {"type": "integer", "description": "First row to fetch (default 0)."},
			"count": {"type": "integer", "description": "Rows to fetch from offset (default 0 = all remaining)."},
			"workspace_root": {"type": "string", "description": "Absolute directory for file-mediated results. Required when the fetched slice exceeds the inline threshold."},
			"inline_row_threshold": {"type": "integer", "description": "Per-call override of the inline threshold (default from config, 100)."}
		},
		"required": ["sid"]
	}`),
}

type getResultsArgs struct {
	SID                string `json:"sid"`
	Offset             int    `json:"offset"`
	Count              int    `json:"count"`
	WorkspaceRoot      string `json:"workspace_root"`
	InlineRowThreshold *int   `json:"inline_row_threshold"`
}

func (d *deps) getResults(ctx context.Context, args json.RawMessage) (any, error) {
	var a getResultsArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.SID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "sid is required")
	}
	if a.Offset < 0 {
		return nil, toolerr.New(toolerr.CodeInvalidArguments, "offset must be >= 0")
	}
	if a.Count < 0 {
		return nil, toolerr.New(toolerr.CodeInvalidArguments, "count must be >= 0 (0 = all remaining)")
	}
	threshold, err := d.threshold(a.InlineRowThreshold)
	if err != nil {
		return nil, err
	}

	status, err := d.client.GetJobStatus(ctx, a.SID)
	if err != nil {
		if isNotFound(err) {
			return nil, toolerr.Newf(toolerr.CodeJobNotFound, "job %s not found (expired TTL or wrong SID)", a.SID).
				WithDetails(map[string]any{"sid": a.SID})
		}
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "job status: %v", err)
	}
	if !status.IsDone {
		return nil, toolerr.Newf(toolerr.CodeJobNotDone,
			"job %s is %s; wait for is_done via check_job before fetching results", a.SID, status.DispatchState).
			WithDetails(map[string]any{"sid": a.SID, "dispatch_state": status.DispatchState})
	}
	if status.DispatchState == "FAILED" {
		msgs := status.FailureMessages()
		return nil, toolerr.Newf(toolerr.CodeJobFailed, "job %s failed", a.SID).
			WithDetails(map[string]any{"sid": a.SID, "error_messages": msgs})
	}

	total := status.ResultCount
	fetch := a.Count
	if fetch <= 0 || fetch > total-a.Offset {
		fetch = max(total-a.Offset, 0)
	}
	if fetch > threshold && a.WorkspaceRoot == "" {
		return nil, workspaceRequired(a.SID, fetch, threshold)
	}

	rows, err := d.client.FetchResults(ctx, a.SID, a.Offset, a.Count, total)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "fetch results: %v", err)
	}
	return d.shapeResults(a.SID, rows, total, a.Offset, threshold, a.WorkspaceRoot)
}
