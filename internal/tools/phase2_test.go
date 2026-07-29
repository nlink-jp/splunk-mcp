package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

func TestListIndexes(t *testing.T) {
	f := &fakeSplunk{
		indexes: []map[string]any{
			{"name": "main", "content": map[string]any{
				"totalEventCount": 1234, "minTime": "2026-01-01T00:00:00+09:00",
				"maxTime": "2026-07-30T00:00:00+09:00", "disabled": false,
			}},
			{"name": "empty_idx", "content": map[string]any{
				"totalEventCount": 0, "disabled": true,
			}},
		},
	}
	d := newTestDeps(t, f, nil)

	out, err := d.listIndexes(context.Background(), nil)
	if err != nil {
		t.Fatalf("listIndexes: %v", err)
	}
	res := out.(map[string]any)
	if res["count"].(int) != 2 {
		t.Errorf("count = %v", res["count"])
	}
	indexes := res["indexes"].([]client.IndexInfo)
	if indexes[0].Name != "main" || indexes[0].EventCount != 1234 {
		t.Errorf("first index = %+v", indexes[0])
	}
	if !indexes[1].Disabled {
		t.Errorf("empty_idx should be disabled: %+v", indexes[1])
	}
}

func TestListSourcetypes_BuildsMetadataSPL(t *testing.T) {
	f := &fakeSplunk{rows: 2}
	d := newTestDeps(t, f, nil)

	_, err := d.listSourcetypes(context.Background(), mustJSON(t, map[string]any{
		"index": "main",
	}))
	if err != nil {
		t.Fatalf("listSourcetypes: %v", err)
	}
	want := "| metadata type=sourcetypes index=main"
	if f.lastSPL != want {
		t.Errorf("SPL = %q, want %q (prepend must not touch a | leader)", f.lastSPL, want)
	}
}

func TestListSourcetypes_DefaultsToAllIndexes(t *testing.T) {
	f := &fakeSplunk{rows: 1}
	d := newTestDeps(t, f, nil)

	if _, err := d.listSourcetypes(context.Background(), nil); err != nil {
		t.Fatalf("listSourcetypes: %v", err)
	}
	if !strings.Contains(f.lastSPL, "index=*") {
		t.Errorf("SPL = %q, want index=*", f.lastSPL)
	}
}

func TestListSourcetypes_RejectsInjection(t *testing.T) {
	f := &fakeSplunk{}
	d := newTestDeps(t, f, nil)

	for _, bad := range []string{
		"main | delete",
		"main]|outputlookup x",
		`main" OR 1=1`,
		"main index=other",
	} {
		_, err := d.listSourcetypes(context.Background(), mustJSON(t, map[string]any{"index": bad}))
		te := asToolErr(t, err)
		if te.Code != toolerr.CodeInvalidArguments {
			t.Errorf("index %q: code = %q, want invalid_arguments", bad, te.Code)
		}
	}
	if f.lastSPL != "" {
		t.Errorf("rejected index must never reach Splunk, got SPL %q", f.lastSPL)
	}
}

func TestListSavedSearches(t *testing.T) {
	f := &fakeSplunk{
		savedSearches: []map[string]any{
			{"name": "Daily Errors", "content": map[string]any{
				"search":      "index=main level=ERROR | stats count",
				"description": "daily error rollup", "disabled": false,
				"is_scheduled": true, "cron_schedule": "0 6 * * *",
			}},
		},
	}
	d := newTestDeps(t, f, nil)

	out, err := d.listSavedSearches(context.Background(), nil)
	if err != nil {
		t.Fatalf("listSavedSearches: %v", err)
	}
	res := out.(map[string]any)
	searches := res["saved_searches"].([]client.SavedSearch)
	if len(searches) != 1 || searches[0].Name != "Daily Errors" {
		t.Fatalf("saved_searches = %+v", searches)
	}
	if !searches[0].IsScheduled || searches[0].CronSchedule != "0 6 * * *" {
		t.Errorf("schedule fields wrong: %+v", searches[0])
	}
}

func TestRunSavedSearch_FullFlow(t *testing.T) {
	f := &fakeSplunk{rows: 4, savedSearchName: "Daily Errors"}
	d := newTestDeps(t, f, nil)

	out, err := d.runSavedSearch(context.Background(), mustJSON(t, map[string]any{
		"name": "Daily Errors", "earliest_time": "-24h", "latest_time": "now",
	}))
	if err != nil {
		t.Fatalf("runSavedSearch: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 4 || len(res.Results) != 4 {
		t.Errorf("total=%d inline=%d, want 4/4", res.TotalRows, len(res.Results))
	}
	if f.lastDispatchName != "Daily Errors" {
		t.Errorf("dispatched name = %q", f.lastDispatchName)
	}
	// Alert actions must never fire, and the window override must be sent.
	if got := f.lastDispatchForm["trigger_actions"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("trigger_actions = %v, want [0]", got)
	}
	if got := f.lastDispatchForm["dispatch.earliest_time"]; len(got) != 1 || got[0] != "-24h" {
		t.Errorf("dispatch.earliest_time = %v", got)
	}
}

func TestRunSavedSearch_NotFound(t *testing.T) {
	f := &fakeSplunk{savedSearchName: "Exists"}
	d := newTestDeps(t, f, nil)

	_, err := d.runSavedSearch(context.Background(), mustJSON(t, map[string]any{"name": "Missing"}))
	te := asToolErr(t, err)
	if te.Code != toolerr.CodeSavedSearchNotFound {
		t.Fatalf("code = %q, want saved_search_not_found", te.Code)
	}
}

func TestRunSavedSearch_MissingName(t *testing.T) {
	d := newTestDeps(t, &fakeSplunk{}, nil)
	_, err := d.runSavedSearch(context.Background(), nil)
	if te := asToolErr(t, err); te.Code != toolerr.CodeMissingArgument {
		t.Fatalf("code = %q, want missing_argument", te.Code)
	}
}
