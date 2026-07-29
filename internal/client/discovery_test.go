package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/config"
)

func TestListIndexes(t *testing.T) {
	var gotCount string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/data/indexes") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotCount = r.URL.Query().Get("count")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": []map[string]any{
				{"name": "main", "content": map[string]any{
					"totalEventCount": 42,
					"minTime":         "2026-01-01T00:00:00+09:00",
					"maxTime":         "2026-07-30T00:00:00+09:00",
					"disabled":        false,
				}},
				// totalEventCount arrives as a string on some versions.
				{"name": "stringy", "content": map[string]any{
					"totalEventCount": "7", "disabled": true,
				}},
			},
		})
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})
	indexes, err := c.ListIndexes(context.Background())
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	if gotCount != "0" {
		t.Errorf("count param = %q, want 0 (no pagination cap)", gotCount)
	}
	if len(indexes) != 2 {
		t.Fatalf("len = %d", len(indexes))
	}
	if indexes[0].Name != "main" || indexes[0].EventCount != 42 || indexes[0].Disabled {
		t.Errorf("main = %+v", indexes[0])
	}
	if indexes[1].EventCount != 7 || !indexes[1].Disabled {
		t.Errorf("stringy = %+v", indexes[1])
	}
}

func TestListSavedSearches(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/saved/searches") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": []map[string]any{
				{"name": "Daily Errors", "content": map[string]any{
					"search":        "index=main level=ERROR",
					"description":   "rollup",
					"disabled":      false,
					"is_scheduled":  true,
					"cron_schedule": "0 6 * * *",
				}},
			},
		})
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})
	searches, err := c.ListSavedSearches(context.Background())
	if err != nil {
		t.Fatalf("ListSavedSearches: %v", err)
	}
	if len(searches) != 1 {
		t.Fatalf("len = %d", len(searches))
	}
	s := searches[0]
	if s.Name != "Daily Errors" || s.Search != "index=main level=ERROR" || !s.IsScheduled {
		t.Errorf("saved search = %+v", s)
	}
}

func TestDispatchSavedSearch(t *testing.T) {
	var gotPath string
	var gotForm map[string][]string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_ = r.ParseForm()
		gotForm = r.Form
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "dispatched_1"})
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok", JobTTL: 5 * time.Minute})
	sid, err := c.DispatchSavedSearch(context.Background(), "My Search/With Special", "-1h", "now")
	if err != nil {
		t.Fatalf("DispatchSavedSearch: %v", err)
	}
	if sid != "dispatched_1" {
		t.Errorf("sid = %q", sid)
	}
	// JoinPath must escape the name (spaces and the embedded slash), so the
	// whole name stays one path segment.
	if !strings.HasSuffix(gotPath, "/saved/searches/My%20Search%2FWith%20Special/dispatch") {
		t.Errorf("escaped path = %q, want .../My%%20Search%%2FWith%%20Special/dispatch", gotPath)
	}
	if got := gotForm["trigger_actions"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("trigger_actions = %v, want [0]", got)
	}
	if got := gotForm["dispatch.earliest_time"]; len(got) != 1 || got[0] != "-1h" {
		t.Errorf("dispatch.earliest_time = %v", got)
	}
	if got := gotForm["dispatch.ttl"]; len(got) != 1 || got[0] != "300" {
		t.Errorf("dispatch.ttl = %v, want [300]", got)
	}
}

func TestDispatchSavedSearch_NotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newTestClient(t, &config.Config{Host: srv.URL, Token: "tok"})
	_, err := c.DispatchSavedSearch(context.Background(), "missing", "", "")
	if !errors.Is(err, ErrSavedSearchNotFound) {
		t.Fatalf("expected ErrSavedSearchNotFound, got %v", err)
	}
}
