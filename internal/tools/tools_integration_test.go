//go:build integration

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/config"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

// liveDeps builds the tool layer against a real Splunk from
// SPLUNK_HOST / SPLUNK_TOKEN env vars. Skipped when unset.
func liveDeps(t *testing.T, mod func(*config.Config)) *deps {
	t.Helper()
	host := os.Getenv("SPLUNK_HOST")
	token := os.Getenv("SPLUNK_TOKEN")
	if host == "" || token == "" {
		t.Skip("SPLUNK_HOST and SPLUNK_TOKEN must be set for integration tests")
	}
	cfg := config.Default()
	cfg.Host = host
	cfg.Token = token
	cfg.Insecure = true // container uses a self-signed cert
	if mod != nil {
		mod(cfg)
	}
	c, err := client.New(cfg, nil)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	c.PollInterval = 500 * time.Millisecond
	return &deps{client: c, cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// TestLive_RunQuery_ExactCountInline is the core-guarantee E2E: the returned
// total_rows must equal the number of rows the SPL actually generates.
func TestLive_RunQuery_ExactCountInline(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| makeresults count=7 | streamstats count as n | fields n`,
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 7 || res.ReturnedRows != 7 || len(res.Results) != 7 {
		t.Errorf("total=%d returned=%d inline=%d, want 7/7/7", res.TotalRows, res.ReturnedRows, len(res.Results))
	}
}

// TestLive_RunQuery_FileMediated verifies the no-truncation contract on a
// real Splunk: every generated row must land in the JSONL file.
func TestLive_RunQuery_FileMediated(t *testing.T) {
	d := liveDeps(t, func(c *config.Config) { c.InlineRowThreshold = 10 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ws := t.TempDir()

	const want = 250
	out, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl":            `| makeresults count=250 | streamstats count as n | fields n`,
		"workspace_root": ws,
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != want || res.ReturnedRows != want {
		t.Errorf("total=%d returned=%d, want %d", res.TotalRows, res.ReturnedRows, want)
	}
	if res.ResultsFile == "" {
		t.Fatal("results_file missing")
	}
	if len(res.Preview) != previewRowCount {
		t.Errorf("preview = %d rows, want %d", len(res.Preview), previewRowCount)
	}

	f, err := os.Open(res.ResultsFile)
	if err != nil {
		t.Fatalf("open results file: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d not valid JSON: %v", lines, err)
		}
		lines++
	}
	if lines != want {
		t.Errorf("results file has %d lines, want %d — truncation would be a design regression", lines, want)
	}
}

// TestLive_RunQuery_WorkspaceRequired checks the pre-fetch guard and that the
// job stays fetchable afterwards via get_results.
func TestLive_RunQuery_WorkspaceRequired(t *testing.T) {
	d := liveDeps(t, func(c *config.Config) { c.InlineRowThreshold = 5 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| makeresults count=20 | streamstats count as n | fields n`,
	}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeWorkspaceRequired {
		t.Fatalf("code = %q, want workspace_required", te.Code)
	}
	sid, _ := te.Details["sid"].(string)
	if sid == "" {
		t.Fatal("details.sid missing")
	}

	// The job is still alive — page a slice inline within the threshold.
	out, err := d.getResults(ctx, mustJSON(t, map[string]any{
		"sid": sid, "offset": 0, "count": 5,
	}))
	if err != nil {
		t.Fatalf("getResults after workspace_required: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 20 || res.ReturnedRows != 5 {
		t.Errorf("total=%d returned=%d, want 20/5", res.TotalRows, res.ReturnedRows)
	}
}

// TestLive_AsyncFlow exercises start_query → check_job → get_results.
func TestLive_AsyncFlow(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := d.startQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| makeresults count=4 | eval tag="async"`,
	}))
	if err != nil {
		t.Fatalf("startQuery: %v", err)
	}
	sid := out.(map[string]any)["sid"].(string)

	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err = d.checkJob(ctx, mustJSON(t, map[string]any{"sid": sid}))
		if err != nil {
			t.Fatalf("checkJob: %v", err)
		}
		if out.(map[string]any)["is_done"].(bool) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish in time")
		}
		time.Sleep(time.Second)
	}
	if got := out.(map[string]any)["result_count"].(int); got != 4 {
		t.Errorf("result_count = %d, want 4", got)
	}

	out, err = d.getResults(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if err != nil {
		t.Fatalf("getResults: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 4 || len(res.Results) != 4 {
		t.Errorf("total=%d inline=%d, want 4/4", res.TotalRows, len(res.Results))
	}
}

// TestLive_CancelJob cancels a slow all-time search.
func TestLive_CancelJob(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	out, err := d.startQuery(ctx, mustJSON(t, map[string]any{
		"spl": "index=* | head 1", "earliest_time": "0", "latest_time": "now",
	}))
	if err != nil {
		t.Fatalf("startQuery: %v", err)
	}
	sid := out.(map[string]any)["sid"].(string)

	out, err = d.cancelJob(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if err != nil {
		t.Fatalf("cancelJob: %v", err)
	}
	if !out.(map[string]any)["cancelled"].(bool) {
		t.Error("cancelled should be true")
	}
}

// TestLive_JobFailed verifies Splunk-side FATAL surfaces as job_failed with
// the message text.
func TestLive_JobFailed(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| thisisnotarealcommand`,
	}))
	if err == nil {
		t.Fatal("expected error for invalid SPL")
	}
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeJobFailed && te.Code != toolerr.CodeSplunkAPI {
		t.Errorf("code = %q, want job_failed or splunk_api_error (submit-time rejection)", te.Code)
	}
	t.Logf("error: %v", te)
}
