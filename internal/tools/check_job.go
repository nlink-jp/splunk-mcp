package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var checkJobTool = mcpserver.Tool{
	Name: "check_job",
	Description: "Check the status of a search job by SID. Returns dispatch state and, once done, " +
		"the exact result count.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"sid": {"type": "string", "description": "Search job SID from start_query or run_query."}
		},
		"required": ["sid"]
	}`),
}

type sidArgs struct {
	SID string `json:"sid"`
}

func (d *deps) checkJob(ctx context.Context, args json.RawMessage) (any, error) {
	var a sidArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.SID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "sid is required")
	}

	status, err := d.client.GetJobStatus(ctx, a.SID)
	if err != nil {
		if isNotFound(err) {
			return nil, toolerr.Newf(toolerr.CodeJobNotFound, "job %s not found (expired TTL or wrong SID)", a.SID).
				WithDetails(map[string]any{"sid": a.SID})
		}
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "job status: %v", err)
	}

	out := map[string]any{
		"sid":            status.SID,
		"is_done":        status.IsDone,
		"dispatch_state": status.DispatchState,
		"result_count":   status.ResultCount,
	}
	if msgs := status.FailureMessages(); len(msgs) > 0 {
		out["error_messages"] = msgs
	}
	if !status.IsDone {
		out["note"] = "result_count is not final until is_done is true"
	}
	return out, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, client.ErrJobNotFound)
}
