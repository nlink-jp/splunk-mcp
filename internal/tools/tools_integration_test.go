//go:build integration

package tools

import (
	"context"
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

// TestLive_RunQuery_MaxRowsCap is the replacement for the old file-mediation
// E2E (ADR-0004 withdrew the spill): a result past the cap comes back capped,
// and the drop is reported instead of being hidden or written to a file.
func TestLive_RunQuery_MaxRowsCap(t *testing.T) {
	d := liveDeps(t, func(c *config.Config) { c.MaxRows = 10 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const total = 250
	out, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| makeresults count=250 | streamstats count as n | fields n`,
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	// The exact count is the guarantee that survives the cap.
	if res.TotalRows != total {
		t.Errorf("total_rows = %d, want %d — the exact count must survive the cap", res.TotalRows, total)
	}
	if res.ReturnedRows != 10 || len(res.Results) != 10 {
		t.Errorf("returned=%d inline=%d, want 10/10", res.ReturnedRows, len(res.Results))
	}
	if !res.Truncated || res.OmittedRows != total-10 {
		t.Errorf("truncated=%v omitted=%d, want true/%d — a quiet cut is the one thing forbidden",
			res.Truncated, res.OmittedRows, total-10)
	}
	if res.Note == "" {
		t.Error("note missing: the drop must be stated in words too")
	}
}

// TestLive_RunQuery_CapThenPage checks that a capped result leaves the job
// fetchable, so the rows the cap dropped are still reachable through paging.
func TestLive_RunQuery_CapThenPage(t *testing.T) {
	d := liveDeps(t, func(c *config.Config) { c.MaxRows = 5 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := d.runQuery(ctx, mustJSON(t, map[string]any{
		"spl": `| makeresults count=20 | streamstats count as n | fields n`,
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	if !res.Truncated || res.TotalRows != 20 || res.ReturnedRows != 5 {
		t.Fatalf("truncated=%v total=%d returned=%d, want true/20/5", res.Truncated, res.TotalRows, res.ReturnedRows)
	}
	if res.SID == "" {
		t.Fatal("sid missing: without it the dropped rows are unreachable")
	}

	// The job is still alive — page the next slice from the same SID.
	out, err = d.getResults(ctx, mustJSON(t, map[string]any{
		"sid": res.SID, "offset": 5, "count": 5,
	}))
	if err != nil {
		t.Fatalf("getResults after a capped run_query: %v", err)
	}
	page := out.(jobResult)
	if page.TotalRows != 20 || page.ReturnedRows != 5 {
		t.Errorf("total=%d returned=%d, want 20/5", page.TotalRows, page.ReturnedRows)
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
