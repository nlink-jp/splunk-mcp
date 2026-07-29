package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var listSavedSearchesTool = mcpserver.Tool{
	Name: "list_saved_searches",
	Description: "List saved searches visible in the configured app/owner namespace, with their SPL " +
		"and schedule. Run one with run_saved_search.",
	InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
}

func (d *deps) listSavedSearches(ctx context.Context, args json.RawMessage) (any, error) {
	searches, err := d.client.ListSavedSearches(ctx)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "list saved searches: %v", err)
	}
	return map[string]any{
		"count":          len(searches),
		"saved_searches": searches,
	}, nil
}
