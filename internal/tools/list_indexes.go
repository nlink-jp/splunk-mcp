package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

var listIndexesTool = mcpserver.Tool{
	Name: "list_indexes",
	Description: "List the event indexes visible to the configured credentials, with event counts " +
		"and event-time bounds. Use before writing SPL to learn what data exists.",
	InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
}

func (d *deps) listIndexes(ctx context.Context, args json.RawMessage) (any, error) {
	indexes, err := d.client.ListIndexes(ctx)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeSplunkAPI, "list indexes: %v", err)
	}
	return map[string]any{
		"count":   len(indexes),
		"indexes": indexes,
	}, nil
}
