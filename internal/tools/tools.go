// Package tools implements the splunk-mcp MCP tools.
//
// Result-delivery contract (shared by run_query and get_results): when the
// row count is at or below the inline threshold the rows are returned inline;
// above it, all rows are written as JSONL under the caller-supplied
// workspace_root and the response carries the file path, a head preview, and
// the exact total. There is deliberately no truncation path — the exact-count
// guarantee is the reason this server exists.
package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/config"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/spl"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

// DefaultWaitSeconds is how long run_query waits for job completion before
// returning wait_timeout (the job keeps running server-side).
const DefaultWaitSeconds = 300.0

// deps carries shared dependencies into the tool handlers.
type deps struct {
	client *client.Client
	cfg    *config.Config
	logger *slog.Logger
}

// Register registers all splunk-mcp tools on the server.
func Register(srv *mcpserver.Server, c *client.Client, cfg *config.Config, logger *slog.Logger) {
	d := &deps{client: c, cfg: cfg, logger: logger}

	srv.RegisterTool(runQueryTool, d.runQuery)
	srv.RegisterTool(startQueryTool, d.startQuery)
	srv.RegisterTool(checkJobTool, d.checkJob)
	srv.RegisterTool(getResultsTool, d.getResults)
	srv.RegisterTool(cancelJobTool, d.cancelJob)
	srv.RegisterTool(listIndexesTool, d.listIndexes)
	srv.RegisterTool(listSourcetypesTool, d.listSourcetypes)
	srv.RegisterTool(listSavedSearchesTool, d.listSavedSearches)
	srv.RegisterTool(runSavedSearchTool, d.runSavedSearch)
	srv.RegisterTool(getUsageTool, d.getUsage)
}

// checkGuard rejects SPL containing blocked destructive commands.
func (d *deps) checkGuard(query string) error {
	if blocked := spl.CheckSafe(query, d.cfg.AllowCommands); blocked != "" {
		return toolerr.Newf(toolerr.CodeUnsafeSPL,
			"SPL contains blocked command %q (write/delete commands are rejected by default; allow it via [server] allow_commands in config if intentional)",
			blocked).
			WithDetails(map[string]any{"blocked_command": blocked})
	}
	return nil
}

// maxRows resolves the cap on rows returned for one call: the caller's own
// max_rows, else the configured default. 0 means no cap.
//
// The caller owns this number, because the caller is the only party that knows
// its context window — the reason this server stopped deciding for it by
// writing files (organization policy, 2026-09-06).
func (d *deps) maxRows(override *int) (int, error) {
	if override == nil {
		return d.cfg.MaxRows, nil
	}
	if *override < 0 {
		return 0, toolerr.New(toolerr.CodeInvalidArguments, "max_rows must be >= 0 (0 means no cap)")
	}
	return *override, nil
}

// jobResult is the shared response shape of run_query and get_results.
type jobResult struct {
	SID          string            `json:"sid"`
	TotalRows    int               `json:"total_rows"`
	Offset       int               `json:"offset,omitempty"`
	ReturnedRows int               `json:"returned_rows"`
	Results      []json.RawMessage `json:"results,omitempty"`
	// Truncated and OmittedRows appear only when the cap dropped something.
	// Their presence is the signal; TotalRows stays exact either way, so a
	// capped answer is still an answer about the whole result set.
	Truncated   bool   `json:"truncated,omitempty"`
	OmittedRows int    `json:"omitted_rows,omitempty"`
	Note        string `json:"note,omitempty"`
}

// shapeResults applies the row cap to fetched rows.
//
// Everything that fits comes back inline; what does not is dropped from the
// response and counted. This server no longer writes results to a file it
// chose: it cannot know the caller's context window, and a runtime that needs
// a large response on disk already puts it there (gem-agent ADR-0058). What is
// never allowed is a quiet cut — total_rows stays exact, and the drop is
// reported in the same result.
func (d *deps) shapeResults(sid string, rows []json.RawMessage, total, offset, maxRows int) jobResult {
	res := jobResult{
		SID:          sid,
		TotalRows:    total,
		Offset:       offset,
		ReturnedRows: len(rows),
	}
	if maxRows == 0 || len(rows) <= maxRows {
		res.Results = rows
		return res
	}
	res.Results = rows[:maxRows]
	res.ReturnedRows = maxRows
	res.Truncated = true
	res.OmittedRows = len(rows) - maxRows
	res.Note = fmt.Sprintf(
		"%d of %d fetched rows were dropped by max_rows (%d). total_rows is exact; page with get_results offset/count, or raise max_rows if your context can hold it.",
		res.OmittedRows, len(rows), maxRows)
	return res
}

func parseArgs(args json.RawMessage, into any) error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(args, into); err != nil {
		return toolerr.Newf(toolerr.CodeInvalidArguments, "invalid arguments: %v", err)
	}
	return nil
}
