package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var cancelJobTool = mcpserver.Tool{
	Name:        "cancel_job",
	Description: "Cancel a running search job by SID.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"sid": {"type": "string", "description": "Search job SID to cancel."}
		},
		"required": ["sid"]
	}`),
}

func (d *deps) cancelJob(ctx context.Context, args json.RawMessage) (any, error) {
	var a sidArgs
	if err := parseArgs(args, &a); err != nil {
		return nil, err
	}
	if a.SID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "sid is required")
	}

	if err := d.client.CancelSearch(ctx, a.SID); err != nil {
		if isNotFound(err) {
			return nil, toolerr.Newf(toolerr.CodeJobNotFound, "job %s not found (already expired or wrong SID)", a.SID).
				WithDetails(map[string]any{"sid": a.SID})
		}
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "cancel job: %v", err)
	}
	return map[string]any{"sid": a.SID, "cancelled": true}, nil
}
