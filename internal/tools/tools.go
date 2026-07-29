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

// previewRowCount is how many head rows accompany a file-mediated result.
const previewRowCount = 5

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

// threshold resolves the effective inline row threshold for a call.
func (d *deps) threshold(override *int) (int, error) {
	if override == nil {
		return d.cfg.InlineRowThreshold, nil
	}
	if *override < 0 {
		return 0, toolerr.New(toolerr.CodeInvalidArguments, "inline_row_threshold must be >= 0")
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
	ResultsFile  string            `json:"results_file,omitempty"`
	Preview      []json.RawMessage `json:"preview,omitempty"`
	Note         string            `json:"note,omitempty"`
}

// shapeResults applies the inline-vs-file contract to fetched rows.
// The caller must have pre-checked workspaceRequired so a large fetch is
// never thrown away here.
func (d *deps) shapeResults(sid string, rows []json.RawMessage, total, offset, threshold int, workspaceRoot string) (jobResult, error) {
	res := jobResult{
		SID:          sid,
		TotalRows:    total,
		Offset:       offset,
		ReturnedRows: len(rows),
	}
	if len(rows) <= threshold {
		res.Results = rows
		return res, nil
	}

	path, err := writeJSONL(workspaceRoot, sid, rows)
	if err != nil {
		return jobResult{}, err
	}
	res.ResultsFile = path
	preview := rows
	if len(preview) > previewRowCount {
		preview = preview[:previewRowCount]
	}
	res.Preview = preview
	res.Note = fmt.Sprintf("%d rows exceed the inline threshold (%d); full result set written as JSONL (one row per line). Preview shows the first %d row(s).",
		len(rows), threshold, len(preview))
	return res, nil
}

// workspaceRequired returns a structured error when a fetch of rowCount rows
// would exceed threshold but no workspace_root was supplied. Called before
// fetching so no work is wasted; the job stays alive for a retry.
func workspaceRequired(sid string, rowCount, threshold int) error {
	return toolerr.Newf(toolerr.CodeWorkspaceRequired,
		"result set has %d rows, above the inline threshold (%d); pass workspace_root (absolute path) to receive the full set as a JSONL file, raise inline_row_threshold, or page with get_results offset/count",
		rowCount, threshold).
		WithDetails(map[string]any{"sid": sid, "total_rows": rowCount})
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
