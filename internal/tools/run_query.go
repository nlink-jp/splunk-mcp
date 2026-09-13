package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var runQueryTool = mcpserver.Tool{
	Name: "run_query",
	Description: "Run a SPL search and wait for completion. Uses Splunk's asynchronous job API " +
		"(never oneshot/preview), so total_rows is always the exact final count. " +
		"Rows come back in the response, capped by max_rows; what the cap leaves out is " +
		"counted in omitted_rows and total_rows stays exact. This server writes no files. " +
		"For searches expected to run long, prefer start_query + check_job + get_results.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"spl": {"type": "string", "description": "SPL query. A leading 'search' command is added according to the configured prepend mode."},
			"earliest_time": {"type": "string", "description": "Search window start, Splunk time modifier (e.g. -24h, @d, 2026-07-01T00:00:00)."},
			"latest_time": {"type": "string", "description": "Search window end, Splunk time modifier (e.g. now)."},
			"wait_seconds": {"type": "number", "description": "Max seconds to wait for completion (default 300). On timeout the job keeps running; poll check_job."},
			"max_rows": {"type": "integer", "description": "Cap on rows returned by this call (default from config, 50000; 0 means no cap). Rows beyond it are dropped from the response and counted in omitted_rows \u2014 total_rows stays exact. Set it to what your context can hold."}
		},
		"required": ["spl"]
	}`),
}

type runQueryArgs struct {
	SPL          string   `json:"spl"`
	EarliestTime string   `json:"earliest_time"`
	LatestTime   string   `json:"latest_time"`
	WaitSeconds  *float64 `json:"wait_seconds"`
	MaxRows      *int     `json:"max_rows"`
}

func (d *deps) runQuery(ctx context.Context, args json.RawMessage) (any, error) {
	var a runQueryArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.SPL == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "spl is required")
	}
	maxRows, err := d.maxRows(a.MaxRows)
	if err != nil {
		return nil, err
	}
	wait := DefaultWaitSeconds
	if a.WaitSeconds != nil && *a.WaitSeconds > 0 {
		wait = *a.WaitSeconds
	}
	if err := d.checkGuard(a.SPL); err != nil {
		return nil, err
	}

	sid, err := d.client.StartSearch(ctx, a.SPL, a.EarliestTime, a.LatestTime)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "start search: %v", err)
	}
	d.logger.Info("search started", "sid", sid)

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(wait*float64(time.Second)))
	defer cancel()
	status, err := d.client.WaitForJob(waitCtx, sid)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, toolerr.Newf(toolerr.CodeWaitTimeout,
				"job still running after %.0fs; it continues server-side — poll check_job, fetch with get_results when done, or cancel_job",
				wait).
				WithDetails(map[string]any{"sid": sid})
		}
		return nil, jobError(sid, err)
	}

	total := status.ResultCount

	rows, err := d.client.FetchResults(ctx, sid, 0, 0, total)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "fetch results: %v", err)
	}
	return d.shapeResults(sid, rows, total, 0, maxRows), nil
}

// jobError maps client-layer errors from a wait/status call onto structured
// tool errors.
func jobError(sid string, err error) error {
	var te *toolerr.Error
	if errors.As(err, &te) {
		return err
	}
	if isNotFound(err) {
		return toolerr.Newf(toolerr.CodeJobNotFound, "job %s not found (expired TTL or wrong SID)", sid).
			WithDetails(map[string]any{"sid": sid})
	}
	return toolerr.Newf(toolerr.CodeJobFailed, "%v", err).
		WithDetails(map[string]any{"sid": sid})
}
