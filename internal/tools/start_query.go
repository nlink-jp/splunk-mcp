package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var startQueryTool = mcpserver.Tool{
	Name: "start_query",
	Description: "Start a SPL search asynchronously and return its SID immediately. " +
		"Use for long-running searches: poll check_job until is_done, then fetch with get_results.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"spl": {"type": "string", "description": "SPL query. A leading 'search' command is added according to the configured prepend mode."},
			"earliest_time": {"type": "string", "description": "Search window start, Splunk time modifier."},
			"latest_time": {"type": "string", "description": "Search window end, Splunk time modifier."}
		},
		"required": ["spl"]
	}`),
}

type startQueryArgs struct {
	SPL          string `json:"spl"`
	EarliestTime string `json:"earliest_time"`
	LatestTime   string `json:"latest_time"`
}

func (d *deps) startQuery(ctx context.Context, args json.RawMessage) (any, error) {
	var a startQueryArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.SPL == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "spl is required")
	}
	if err := d.checkGuard(a.SPL); err != nil {
		return nil, err
	}

	sid, err := d.client.StartSearch(ctx, a.SPL, a.EarliestTime, a.LatestTime)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "start search: %v", err)
	}
	d.logger.Info("search started", "sid", sid)
	return map[string]any{
		"sid":  sid,
		"note": "job started; poll check_job until is_done, then call get_results",
	}, nil
}
