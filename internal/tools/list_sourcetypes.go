package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var listSourcetypesTool = mcpserver.Tool{
	Name: "list_sourcetypes",
	Description: "List sourcetypes (with event counts and time bounds) via '| metadata type=sourcetypes'. " +
		"Runs as a search job, so counts are exact for the given index and time window.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"index": {"type": "string", "description": "Index to inspect (default '*' = all searchable indexes). Wildcards allowed."},
			"earliest_time": {"type": "string", "description": "Window start, Splunk time modifier (default: index lifetime)."},
			"latest_time": {"type": "string", "description": "Window end, Splunk time modifier."},
			"workspace_root": {"type": "string", "description": "Absolute directory for file-mediated results (only needed for very large sourcetype sets)."},
			"inline_row_threshold": {"type": "integer", "description": "Per-call override of the inline threshold."}
		}
	}`),
}

// indexNamePattern is deliberately strict: metadata's index argument is
// interpolated into SPL, so anything outside plain index-name characters
// (plus the * wildcard) is rejected to keep injection impossible.
var indexNamePattern = regexp.MustCompile(`^[A-Za-z0-9_*-]+$`)

type listSourcetypesArgs struct {
	Index              string `json:"index"`
	EarliestTime       string `json:"earliest_time"`
	LatestTime         string `json:"latest_time"`
	WorkspaceRoot      string `json:"workspace_root"`
	InlineRowThreshold *int   `json:"inline_row_threshold"`
}

func (d *deps) listSourcetypes(ctx context.Context, args json.RawMessage) (any, error) {
	var a listSourcetypesArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	index := a.Index
	if index == "" {
		index = "*"
	}
	if !indexNamePattern.MatchString(index) {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments,
			"index must match %s (got %q)", indexNamePattern.String(), index)
	}
	threshold, err := d.threshold(a.InlineRowThreshold)
	if err != nil {
		return nil, err
	}

	// Leading "|" means the prepend modes all leave this SPL untouched.
	spl := fmt.Sprintf(`| metadata type=sourcetypes index=%s`, index)

	sid, err := d.client.StartSearch(ctx, spl, a.EarliestTime, a.LatestTime)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "start metadata search: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(DefaultWaitSeconds*float64(time.Second)))
	defer cancel()
	status, err := d.client.WaitForJob(waitCtx, sid)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, toolerr.Newf(toolerr.CodeWaitTimeout,
				"metadata search still running; poll check_job").
				WithDetails(map[string]any{"sid": sid})
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
