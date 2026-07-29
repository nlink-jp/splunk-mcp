package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var runSavedSearchTool = mcpserver.Tool{
	Name: "run_saved_search",
	Description: "Dispatch a saved search by name, wait for completion, and return results under " +
		"the same exact-count / file-mediation contract as run_query. Alert actions are never " +
		"triggered. The saved SPL is server-defined and runs as-is (the destructive-command guard " +
		"applies only to ad-hoc SPL).",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Saved search name (see list_saved_searches)."},
			"earliest_time": {"type": "string", "description": "Override the saved dispatch window start (Splunk time modifier)."},
			"latest_time": {"type": "string", "description": "Override the saved dispatch window end."},
			"wait_seconds": {"type": "number", "description": "Max seconds to wait for completion (default 300). On timeout the job keeps running; poll check_job."},
			"workspace_root": {"type": "string", "description": "Absolute directory for file-mediated results."},
			"inline_row_threshold": {"type": "integer", "description": "Per-call override of the inline threshold."}
		},
		"required": ["name"]
	}`),
}

type runSavedSearchArgs struct {
	Name               string   `json:"name"`
	EarliestTime       string   `json:"earliest_time"`
	LatestTime         string   `json:"latest_time"`
	WaitSeconds        *float64 `json:"wait_seconds"`
	WorkspaceRoot      string   `json:"workspace_root"`
	InlineRowThreshold *int     `json:"inline_row_threshold"`
}

func (d *deps) runSavedSearch(ctx context.Context, args json.RawMessage) (any, error) {
	var a runSavedSearchArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Name == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "name is required")
	}
	threshold, err := d.threshold(a.InlineRowThreshold)
	if err != nil {
		return nil, err
	}
	wait := DefaultWaitSeconds
	if a.WaitSeconds != nil && *a.WaitSeconds > 0 {
		wait = *a.WaitSeconds
	}

	sid, err := d.client.DispatchSavedSearch(ctx, a.Name, a.EarliestTime, a.LatestTime)
	if err != nil {
		if errors.Is(err, client.ErrSavedSearchNotFound) {
			return nil, toolerr.Newf(toolerr.CodeSavedSearchNotFound,
				"saved search %q not found in the configured app/owner namespace (see list_saved_searches)", a.Name).
				WithDetails(map[string]any{"name": a.Name})
		}
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "dispatch saved search: %v", err)
	}
	d.logger.Info("saved search dispatched", "name", a.Name, "sid", sid)

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(wait*float64(time.Second)))
	defer cancel()
	status, err := d.client.WaitForJob(waitCtx, sid)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, toolerr.Newf(toolerr.CodeWaitTimeout,
				"saved search %q still running after %.0fs; poll check_job, fetch with get_results when done, or cancel_job",
				a.Name, wait).
				WithDetails(map[string]any{"sid": sid, "name": a.Name})
		}
		return nil, jobError(sid, err)
	}

	total := status.ResultCount
	if total > threshold && a.WorkspaceRoot == "" {
		return nil, workspaceRequired(sid, total, threshold)
	}
	rows, err := d.client.FetchResults(ctx, sid, 0, 0, total)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "fetch results: %v", err)
	}
	return d.shapeResults(sid, rows, total, 0, threshold, a.WorkspaceRoot)
}
