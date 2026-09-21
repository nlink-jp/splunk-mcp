// Package tools implements the splunk-mcp MCP tools.
//
// Result-delivery contract (shared by run_query and get_results): rows come
// back in the response, capped by max_rows, and what the cap leaves out is
// counted in omitted_rows while total_rows stays exact. This server writes no
// result files: it cannot know the caller's context window, so file-ising a
// large response is the runtime's job (ADR-0004, organization ADR-021). The
// exact count is the guarantee that survives — it is why this server exists.
package tools

import (
	"bytes"
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

// parseArgs decodes a tool's arguments strictly: an argument the tool does not
// declare is refused by name. Every tool decodes through here, including the
// ones that take no arguments — "none" still means none, not any.
//
// The closed `additionalProperties: false` on each descriptor's InputSchema is
// only the declared half of org ADR-021 §4 — what a schema-checking client
// refuses before the call. This is the half that actually refuses, and it is
// needed because not every client checks the schema and a caller speaking
// JSON-RPC directly checks nothing. Without it a misspelt `max_rows` left the
// cap unbound and the call fell back to the configured default while reading
// as though the caller's number had been honoured — which undercuts the exact
// count this server exists to give.
func parseArgs(args json.RawMessage, into any) error {
	trimmed := bytes.TrimSpace(args)
	// Omitted or null arguments mean the empty object, not an error: a tool
	// whose arguments are all optional is legitimately called with none.
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return toolerr.Newf(toolerr.CodeInvalidArguments, "invalid arguments: %v", err)
	}
	return nil
}
