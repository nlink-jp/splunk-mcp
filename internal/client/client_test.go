package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/config"
)

func newTestClient(t *testing.T, cfg *config.Config) *Client {
	t.Helper()
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.PollInterval = 5 * time.Millisecond
	return c
}

func TestStartSearch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "search/jobs") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "test_sid_123"})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	sid, err := c.StartSearch(context.Background(), "index=main", "-1h", "now")
	if err != nil {
		t.Fatalf("StartSearch: %v", err)
	}
	if sid != "test_sid_123" {
		t.Errorf("SID = %q, want %q", sid, "test_sid_123")
	}
}

func TestStartSearch_PipePrefix(t *testing.T) {
	var gotSearch string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSearch = r.FormValue("search")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "sid1"})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	// SPL starting with | should NOT get "search " prefix.
	_, _ = c.StartSearch(context.Background(), "| stats count", "", "")
	if gotSearch != "| stats count" {
		t.Errorf("pipe SPL should not get prefix, got: %q", gotSearch)
	}

	// Normal SPL should get "search " prefix.
	_, _ = c.StartSearch(context.Background(), "index=main", "", "")
	if gotSearch != "search index=main" {
		t.Errorf("normal SPL should get prefix, got: %q", gotSearch)
	}
}

func TestStartSearch_JobTTL(t *testing.T) {
	var gotTimeout string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotTimeout = r.FormValue("timeout")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "sid1"})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok", JobTTL: 10 * time.Minute})
	_, _ = c.StartSearch(context.Background(), "index=main", "", "")
	if gotTimeout != "600" {
		t.Errorf("timeout param = %q, want %q", gotTimeout, "600")
	}

	// Without JobTTL the param must be absent.
	c2 := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})
	gotTimeout = "unset-sentinel"
	_, _ = c2.StartSearch(context.Background(), "index=main", "", "")
	if gotTimeout != "" {
		t.Errorf("timeout param should be absent, got %q", gotTimeout)
	}
}

func TestGetJobStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": []map[string]any{
				{"content": map[string]any{
					"isDone":        true,
					"dispatchState": "DONE",
					"resultCount":   42,
					"messages":      []any{},
				}},
			},
		})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	status, err := c.GetJobStatus(context.Background(), "sid1")
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if !status.IsDone {
		t.Error("IsDone should be true")
	}
	if status.DispatchState != "DONE" {
		t.Errorf("DispatchState = %q", status.DispatchState)
	}
	if status.ResultCount != 42 {
		t.Errorf("ResultCount = %d", status.ResultCount)
	}
}

func TestGetJobStatus_NotFound404(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetJobStatus(context.Background(), "missing")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestGetJobStatus_EmptyEntry(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": []any{}})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetJobStatus(context.Background(), "missing")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestGetJobStatus_APIError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized"))
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.GetJobStatus(context.Background(), "sid1"); err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestWaitForJob_ReturnsFinalStatus(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		done := calls >= 3
		state := "RUNNING"
		if done {
			state = "DONE"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": []map[string]any{
				{"content": map[string]any{
					"isDone":        done,
					"dispatchState": state,
					"resultCount":   7,
					"messages":      []any{},
				}},
			},
		})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	status, err := c.WaitForJob(context.Background(), "sid1")
	if err != nil {
		t.Fatalf("WaitForJob: %v", err)
	}
	if status.ResultCount != 7 {
		t.Errorf("ResultCount = %d", status.ResultCount)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 polls, got %d", calls)
	}
}

func TestWaitForJob_Failed(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": []map[string]any{
				{"content": map[string]any{
					"isDone":        true,
					"dispatchState": "FAILED",
					"resultCount":   0,
					"messages": []map[string]string{
						{"type": "FATAL", "text": "Unknown search command 'frobnicate'."},
					},
				}},
			},
		})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	_, err := c.WaitForJob(context.Background(), "sid1")
	if err == nil {
		t.Fatal("expected error for FAILED job")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error should carry the FATAL message: %v", err)
	}
}

func TestFetchResults_Pagination(t *testing.T) {
	// 7 rows total; serve pages honoring offset/count so a small page size
	// exercises the paging loop. maxResultsPerPage is large, so instead the
	// server enforces its own cap of 3 rows per request — FetchResults must
	// still collect everything because it advances by len(page).
	total := 7
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		if count > 3 {
			count = 3 // server-side cap, like limits.conf
		}
		var rows []map[string]any
		for i := offset; i < offset+count && i < total; i++ {
			rows = append(rows, map[string]any{"n": i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	rows, err := c.FetchResults(context.Background(), "sid1", 0, 0, total)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(rows) != total {
		t.Fatalf("got %d rows, want %d", len(rows), total)
	}
	// Verify ordering across page boundaries.
	for i, raw := range rows {
		var row struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if row.N != i {
			t.Errorf("row %d = %d, want %d", i, row.N, i)
		}
	}
}

func TestFetchResults_OffsetAndCount(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		var rows []map[string]any
		for i := offset; i < offset+count && i < 100; i++ {
			rows = append(rows, map[string]any{"n": i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	rows, err := c.FetchResults(context.Background(), "sid1", 10, 5, 100)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	var first struct {
		N int `json:"n"`
	}
	_ = json.Unmarshal(rows[0], &first)
	if first.N != 10 {
		t.Errorf("first row = %d, want 10", first.N)
	}
}

func TestFetchResults_CountBeyondTotal(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		var rows []map[string]any
		for i := offset; i < offset+count && i < 3; i++ {
			rows = append(rows, map[string]any{"n": i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	rows, err := c.FetchResults(context.Background(), "sid1", 0, 50, 3)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("got %d rows, want 3 (clamped to total)", len(rows))
	}
}

func TestCancelSearch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "control") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.FormValue("action") != "cancel" {
			t.Errorf("expected action=cancel, got %q", r.FormValue("action"))
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	if err := c.CancelSearch(context.Background(), "sid1"); err != nil {
		t.Fatalf("CancelSearch: %v", err)
	}
}

func TestCancelSearch_NotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})

	err := c.CancelSearch(context.Background(), "missing")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestAppContextPath(t *testing.T) {
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "sid1"})
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok", App: "myapp"})
	_, _ = c.StartSearch(context.Background(), "index=main", "", "")
	want := "/servicesNS/nobody/myapp/search/jobs"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

var _ = fmt.Sprintf
