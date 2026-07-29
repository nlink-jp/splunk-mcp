package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/config"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

// fakeSplunk is a minimal in-memory Splunk REST backend for one job.
type fakeSplunk struct {
	mu             sync.Mutex
	rows           int // total result rows the job "finds"
	pollsUntilDone int // status calls before isDone flips true
	neverDone      bool
	failed         bool
	failText       string

	polls        int
	resultsCalls int
	cancelCalls  int
	lastSPL      string
	sid          string
}

func (f *fakeSplunk) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/search/jobs"):
			_ = r.ParseForm()
			f.lastSPL = r.FormValue("search")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"sid": f.sid})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/control"):
			if !strings.Contains(r.URL.Path, f.sid) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.cancelCalls++
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/results"):
			if !strings.Contains(r.URL.Path, f.sid) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.resultsCalls++
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			count, _ := strconv.Atoi(r.URL.Query().Get("count"))
			var rows []map[string]any
			for i := offset; i < offset+count && i < f.rows; i++ {
				rows = append(rows, map[string]any{"n": i, "host": fmt.Sprintf("host%d", i%3)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/search/jobs/"):
			if !strings.Contains(r.URL.Path, f.sid) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.polls++
			done := !f.neverDone && f.polls >= f.pollsUntilDone
			state := "RUNNING"
			msgs := []map[string]string{}
			if done {
				state = "DONE"
			}
			if f.failed {
				done, state = true, "FAILED"
				msgs = append(msgs, map[string]string{"type": "FATAL", "text": f.failText})
			}
			count := 0
			if done {
				count = f.rows
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entry": []map[string]any{
					{"content": map[string]any{
						"isDone":        done,
						"dispatchState": state,
						"resultCount":   count,
						"messages":      msgs,
					}},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// newTestDeps wires deps against a fakeSplunk with a fast poll interval.
func newTestDeps(t *testing.T, f *fakeSplunk, mod func(*config.Config)) *deps {
	t.Helper()
	if f.sid == "" {
		f.sid = "sid_test_1"
	}
	if f.pollsUntilDone == 0 {
		f.pollsUntilDone = 1
	}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.Host = srv.URL
	cfg.Token = "tok"
	if mod != nil {
		mod(cfg)
	}
	c, err := client.New(cfg, nil)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	c.PollInterval = 2 * time.Millisecond
	return &deps{client: c, cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asToolErr(t *testing.T, err error) *toolerr.Error {
	t.Helper()
	var te *toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("expected *toolerr.Error, got %T: %v", err, err)
	}
	return te
}

func TestRunQuery_Inline(t *testing.T) {
	f := &fakeSplunk{rows: 7, pollsUntilDone: 2}
	d := newTestDeps(t, f, nil)

	out, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{
		"spl": "index=main | stats count by host",
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 7 || res.ReturnedRows != 7 {
		t.Errorf("TotalRows=%d ReturnedRows=%d, want 7/7", res.TotalRows, res.ReturnedRows)
	}
	if len(res.Results) != 7 {
		t.Errorf("inline results = %d rows", len(res.Results))
	}
	if res.ResultsFile != "" {
		t.Errorf("unexpected results_file for inline result: %q", res.ResultsFile)
	}
	if !strings.HasPrefix(f.lastSPL, "search index=main") {
		t.Errorf("prepend not applied: %q", f.lastSPL)
	}
}

func TestRunQuery_FileMediated(t *testing.T) {
	f := &fakeSplunk{rows: 12}
	d := newTestDeps(t, f, func(c *config.Config) { c.InlineRowThreshold = 5 })
	ws := t.TempDir()

	out, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{
		"spl":            "index=main",
		"workspace_root": ws,
	}))
	if err != nil {
		t.Fatalf("runQuery: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 12 || res.ReturnedRows != 12 {
		t.Errorf("TotalRows=%d ReturnedRows=%d, want 12/12", res.TotalRows, res.ReturnedRows)
	}
	if res.Results != nil {
		t.Error("inline results should be empty for file-mediated response")
	}
	if len(res.Preview) != previewRowCount {
		t.Errorf("preview = %d rows, want %d", len(res.Preview), previewRowCount)
	}
	if res.ResultsFile == "" {
		t.Fatal("results_file missing")
	}

	// The JSONL file holds every row, in order, one JSON object per line.
	file, err := os.Open(res.ResultsFile)
	if err != nil {
		t.Fatalf("open results file: %v", err)
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	line := 0
	for sc.Scan() {
		var row struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d: %v", line, err)
		}
		if row.N != line {
			t.Errorf("line %d has n=%d", line, row.N)
		}
		line++
	}
	if line != 12 {
		t.Errorf("results file has %d lines, want 12", line)
	}
}

func TestRunQuery_WorkspaceRequired(t *testing.T) {
	f := &fakeSplunk{rows: 500}
	d := newTestDeps(t, f, nil) // default threshold 100

	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{"spl": "index=main"}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeWorkspaceRequired {
		t.Fatalf("code = %q, want workspace_required", te.Code)
	}
	if te.Details["total_rows"] != 500 {
		t.Errorf("details.total_rows = %v", te.Details["total_rows"])
	}
	if te.Details["sid"] == "" {
		t.Error("details.sid missing — agent needs it to retry via get_results")
	}
	// The check fires before any fetch, so no rows were wasted.
	if f.resultsCalls != 0 {
		t.Errorf("results endpoint called %d times; want 0", f.resultsCalls)
	}
}

func TestRunQuery_GuardBlocks(t *testing.T) {
	f := &fakeSplunk{}
	d := newTestDeps(t, f, nil)

	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{
		"spl": "index=main | delete",
	}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeUnsafeSPL {
		t.Fatalf("code = %q, want unsafe_spl", te.Code)
	}
	if f.lastSPL != "" {
		t.Error("blocked SPL must never reach Splunk")
	}
}

func TestRunQuery_GuardAllowlist(t *testing.T) {
	f := &fakeSplunk{rows: 1}
	d := newTestDeps(t, f, func(c *config.Config) { c.AllowCommands = []string{"collect"} })

	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{
		"spl": "index=main | collect index=summary",
	}))
	if err != nil {
		t.Fatalf("allowed command should pass: %v", err)
	}
}

func TestRunQuery_WaitTimeout(t *testing.T) {
	f := &fakeSplunk{neverDone: true}
	d := newTestDeps(t, f, nil)

	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{
		"spl":          "index=main",
		"wait_seconds": 0.02,
	}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeWaitTimeout {
		t.Fatalf("code = %q, want wait_timeout", te.Code)
	}
	if te.Details["sid"] == "" {
		t.Error("details.sid missing — agent needs it to poll check_job")
	}
	// The job must NOT be cancelled on wait timeout; it keeps running.
	if f.cancelCalls != 0 {
		t.Errorf("cancel called %d times on wait timeout; want 0", f.cancelCalls)
	}
}

func TestRunQuery_JobFailed(t *testing.T) {
	f := &fakeSplunk{failed: true, failText: "Unknown search command 'frobnicate'."}
	d := newTestDeps(t, f, nil)

	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{"spl": "index=main | frobnicate"}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeJobFailed {
		t.Fatalf("code = %q, want job_failed", te.Code)
	}
	if !strings.Contains(te.Message, "frobnicate") {
		t.Errorf("message should carry Splunk's FATAL text: %q", te.Message)
	}
}

func TestRunQuery_MissingSPL(t *testing.T) {
	d := newTestDeps(t, &fakeSplunk{}, nil)
	_, err := d.runQuery(context.Background(), mustJSON(t, map[string]any{}))
	if te := asToolErr(t, err); te.Code != toolerr.CodeMissingArgument {
		t.Fatalf("code = %q, want missing_argument", te.Code)
	}
}

func TestAsyncFlow_StartCheckResults(t *testing.T) {
	// Each of checkJob and getResults consumes one status poll, so the job
	// flips to done on the third status call: check(1) → getResults(2,
	// still running) → check(3, done).
	f := &fakeSplunk{rows: 3, pollsUntilDone: 3}
	d := newTestDeps(t, f, nil)
	ctx := context.Background()

	// start_query
	out, err := d.startQuery(ctx, mustJSON(t, map[string]any{"spl": "index=main"}))
	if err != nil {
		t.Fatalf("startQuery: %v", err)
	}
	sid := out.(map[string]any)["sid"].(string)
	if sid == "" {
		t.Fatal("empty sid")
	}

	// check_job: first poll not done.
	out, err = d.checkJob(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if err != nil {
		t.Fatalf("checkJob: %v", err)
	}
	st := out.(map[string]any)
	if st["is_done"].(bool) {
		t.Fatal("job should not be done on first poll")
	}

	// get_results while running → job_not_done.
	_, err = d.getResults(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if te := asToolErr(t, err); te.Code != toolerr.CodeJobNotDone {
		t.Fatalf("code = %q, want job_not_done", te.Code)
	}

	// Second poll: done with final count.
	out, err = d.checkJob(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if err != nil {
		t.Fatalf("checkJob: %v", err)
	}
	st = out.(map[string]any)
	if !st["is_done"].(bool) || st["result_count"].(int) != 3 {
		t.Fatalf("unexpected final status: %v", st)
	}

	// get_results now succeeds.
	out, err = d.getResults(ctx, mustJSON(t, map[string]any{"sid": sid}))
	if err != nil {
		t.Fatalf("getResults: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 3 || len(res.Results) != 3 {
		t.Errorf("TotalRows=%d inline=%d, want 3/3", res.TotalRows, len(res.Results))
	}
}

func TestGetResults_OffsetCount(t *testing.T) {
	f := &fakeSplunk{rows: 50}
	d := newTestDeps(t, f, nil)

	out, err := d.getResults(context.Background(), mustJSON(t, map[string]any{
		"sid": "sid_test_1", "offset": 10, "count": 5,
	}))
	if err != nil {
		t.Fatalf("getResults: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 50 {
		t.Errorf("TotalRows = %d, want 50 (exact job total, not slice size)", res.TotalRows)
	}
	if res.Offset != 10 || res.ReturnedRows != 5 {
		t.Errorf("Offset=%d ReturnedRows=%d, want 10/5", res.Offset, res.ReturnedRows)
	}
	var first struct {
		N int `json:"n"`
	}
	_ = json.Unmarshal(res.Results[0], &first)
	if first.N != 10 {
		t.Errorf("first row n=%d, want 10", first.N)
	}
}

func TestGetResults_UnknownSID(t *testing.T) {
	d := newTestDeps(t, &fakeSplunk{rows: 1}, nil)
	_, err := d.getResults(context.Background(), mustJSON(t, map[string]any{"sid": "nope"}))
	if te := asToolErr(t, err); te.Code != toolerr.CodeJobNotFound {
		t.Fatalf("code = %q, want job_not_found", te.Code)
	}
}

func TestCancelJob(t *testing.T) {
	f := &fakeSplunk{}
	d := newTestDeps(t, f, nil)

	out, err := d.cancelJob(context.Background(), mustJSON(t, map[string]any{"sid": "sid_test_1"}))
	if err != nil {
		t.Fatalf("cancelJob: %v", err)
	}
	if !out.(map[string]any)["cancelled"].(bool) {
		t.Error("cancelled should be true")
	}
	if f.cancelCalls != 1 {
		t.Errorf("cancel endpoint called %d times, want 1", f.cancelCalls)
	}
}

func TestGetUsage(t *testing.T) {
	d := newTestDeps(t, &fakeSplunk{}, nil)
	out, err := d.getUsage(context.Background(), nil)
	if err != nil {
		t.Fatalf("getUsage: %v", err)
	}
	// getUsage returns a RawResult with one text block.
	rr, ok := out.(mcpserver.RawResult)
	if !ok {
		t.Fatalf("expected mcpserver.RawResult, got %T", out)
	}
	if len(rr.Content) != 1 || rr.Content[0].Type != "text" {
		t.Fatalf("expected one text content block, got %+v", rr.Content)
	}
	text := rr.Content[0].Text
	for _, want := range []string{"run_query", "start_query", "check_job", "get_results", "cancel_job", "workspace_required"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage text missing %q", want)
		}
	}
}
