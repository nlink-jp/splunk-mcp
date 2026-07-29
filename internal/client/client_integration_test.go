//go:build integration

package client

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/config"
)

// integrationClient builds a Client from SPLUNK_HOST / SPLUNK_TOKEN env vars.
// The test is skipped if either variable is unset.
func integrationClient(t *testing.T) *Client {
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
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.PollInterval = 500 * time.Millisecond
	return c
}

// TestIntegration_SearchLifecycle runs the full lifecycle:
//
//	StartSearch → WaitForJob → FetchResults (exact count, all rows)
func TestIntegration_SearchLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A simple generating SPL that always produces exactly 3 rows.
	spl := `| makeresults count=3 | eval msg="hello"`

	sid, err := c.StartSearch(ctx, spl, "", "")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	t.Logf("SID: %s", sid)

	status, err := c.WaitForJob(ctx, sid)
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if !status.IsDone || status.DispatchState != "DONE" {
		t.Errorf("status = %+v, want DONE", status)
	}
	if status.ResultCount != 3 {
		t.Errorf("ResultCount = %d, want 3", status.ResultCount)
	}

	rows, err := c.FetchResults(ctx, sid, 0, 0, status.ResultCount)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("len(rows) = %d, want 3", len(rows))
	}
	var row struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		t.Fatalf("row JSON invalid: %v", err)
	}
	if row.Msg != "hello" {
		t.Errorf("msg = %q, want hello", row.Msg)
	}
}

// TestIntegration_OffsetCount checks offset/count paging against a live job.
func TestIntegration_OffsetCount(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spl := `| makeresults count=10 | streamstats count as n | fields n`
	sid, err := c.StartSearch(ctx, spl, "", "")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	status, err := c.WaitForJob(ctx, sid)
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if status.ResultCount != 10 {
		t.Fatalf("ResultCount = %d, want 10", status.ResultCount)
	}

	rows, err := c.FetchResults(ctx, sid, 4, 3, status.ResultCount)
	if err != nil {
		t.Fatalf("FetchResults(offset=4,count=3): %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("len(rows) = %d, want 3", len(rows))
	}
}

// TestIntegration_EmptyResults checks that a zero-result job yields no rows.
func TestIntegration_EmptyResults(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// makeresults requires count >= 1, so filter everything out instead.
	spl := `| makeresults count=1 | where 1=0`
	sid, err := c.StartSearch(ctx, spl, "", "")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	status, err := c.WaitForJob(ctx, sid)
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if status.ResultCount != 0 {
		t.Errorf("ResultCount = %d, want 0", status.ResultCount)
	}
	rows, err := c.FetchResults(ctx, sid, 0, 0, status.ResultCount)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(rows))
	}
}

// TestIntegration_CancelSearch starts a long-running job and cancels it.
func TestIntegration_CancelSearch(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	// A search over all time that won't finish quickly.
	spl := `search index=* | head 1`
	sid, err := c.StartSearch(ctx, spl, "0", "now")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	t.Logf("Started job %s", sid)

	if err := c.CancelSearch(ctx, sid); err != nil {
		t.Fatalf("CancelSearch: %v", err)
	}

	// After cancel, job should not be in a running state.
	status, err := c.GetJobStatus(ctx, sid)
	if err != nil {
		// 404 after cancel is also acceptable behaviour.
		t.Logf("GetJobStatus after cancel: %v (may be expected)", err)
		return
	}
	if status.DispatchState == "RUNNING" {
		t.Errorf("job still RUNNING after cancel")
	}
	t.Logf("Post-cancel state: %s", status.DispatchState)
}

// TestIntegration_InvalidSPL checks that a syntax error surfaces as an error.
func TestIntegration_InvalidSPL(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spl := `| thisisnotarealcommand`
	sid, err := c.StartSearch(ctx, spl, "", "")
	if err != nil {
		// Some Splunk versions reject bad SPL at submit time — that's fine.
		t.Logf("StartSearch rejected invalid SPL at submit: %v", err)
		return
	}

	if _, err := c.WaitForJob(ctx, sid); err == nil {
		t.Error("expected WaitForJob to fail for invalid SPL, got nil")
	} else {
		t.Logf("WaitForJob correctly returned error: %v", err)
	}
}

// TestIntegration_SearchPrefix verifies that bare SPL gets "search " prepended.
func TestIntegration_SearchPrefix(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// This SPL is valid only if the "search " prefix is added.
	spl := `index=_internal | head 1`
	sid, err := c.StartSearch(ctx, spl, "-1m", "now")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	status, err := c.WaitForJob(ctx, sid)
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if status.DispatchState == "FAILED" {
		t.Errorf("search with bare SPL failed — prefix may not have been added")
	}
	t.Logf("DispatchState=%s ResultCount=%d", status.DispatchState, status.ResultCount)
}

// TestIntegration_JobTTL verifies the TTL param is accepted at job creation.
func TestIntegration_JobTTL(t *testing.T) {
	host := os.Getenv("SPLUNK_HOST")
	token := os.Getenv("SPLUNK_TOKEN")
	if host == "" || token == "" {
		t.Skip("SPLUNK_HOST and SPLUNK_TOKEN must be set for integration tests")
	}
	cfg := config.Default()
	cfg.Host = host
	cfg.Token = token
	cfg.Insecure = true
	cfg.JobTTL = 10 * time.Minute
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.PollInterval = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sid, err := c.StartSearch(ctx, `| makeresults count=1`, "", "")
	if err != nil {
		t.Fatalf("StartSearch with JobTTL: %v", err)
	}
	if _, err := c.WaitForJob(ctx, sid); err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	// Job must still be retrievable right after completion.
	if _, err := c.GetJobStatus(ctx, sid); err != nil {
		t.Errorf("GetJobStatus after completion: %v", err)
	}
	_ = strings.TrimSpace // keep strings import for future assertions
}
