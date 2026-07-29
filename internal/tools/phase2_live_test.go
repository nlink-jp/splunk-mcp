//go:build integration

package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/client"
)

func TestLive_ListIndexes(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	out, err := d.listIndexes(ctx, nil)
	if err != nil {
		t.Fatalf("listIndexes: %v", err)
	}
	res := out.(map[string]any)
	indexes := res["indexes"].([]client.IndexInfo)
	if len(indexes) == 0 {
		t.Fatal("no indexes returned")
	}
	found := false
	for _, ix := range indexes {
		if ix.Name == "main" {
			found = true
		}
	}
	if !found {
		t.Errorf("index 'main' not in listing: %+v", indexes)
	}
}

func TestLive_ListSourcetypes(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// _internal always has splunkd logs in a running container.
	out, err := d.listSourcetypes(ctx, mustJSON(t, map[string]any{"index": "_internal"}))
	if err != nil {
		t.Fatalf("listSourcetypes: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows == 0 || len(res.Results) == 0 {
		t.Fatalf("expected sourcetypes in _internal, got %+v", res)
	}
	var row struct {
		Sourcetype string `json:"sourcetype"`
	}
	if err := json.Unmarshal(res.Results[0], &row); err != nil || row.Sourcetype == "" {
		t.Errorf("first row lacks sourcetype field: %s (err=%v)", res.Results[0], err)
	}
}

// liveSavedSearchFixture creates a throwaway saved search directly over REST
// and removes it on cleanup.
func liveSavedSearchFixture(t *testing.T, name, spl string) {
	t.Helper()
	host := os.Getenv("SPLUNK_HOST")
	token := os.Getenv("SPLUNK_TOKEN")

	// Test-only client for the local container, which provisions a fresh
	// self-signed cert on every boot — there is no stable CA to trust.
	// Production connections verify TLS unless [splunk] insecure is set.
	httpc := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   30 * time.Second,
	}
	form := url.Values{}
	form.Set("name", name)
	form.Set("search", spl)
	form.Set("output_mode", "json")
	req, _ := http.NewRequest(http.MethodPost, host+"/services/saved/searches", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatalf("create saved search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create saved search: HTTP %d", resp.StatusCode)
	}

	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete,
			host+"/services/saved/searches/"+url.PathEscape(name), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if resp, err := httpc.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	})
}

func TestLive_SavedSearches_ListAndRun(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const fixture = "splunk-mcp integration fixture"
	liveSavedSearchFixture(t, fixture, `| makeresults count=2 | eval src="fixture"`)

	// list_saved_searches must show the fixture with its SPL.
	out, err := d.listSavedSearches(ctx, nil)
	if err != nil {
		t.Fatalf("listSavedSearches: %v", err)
	}
	searches := out.(map[string]any)["saved_searches"].([]client.SavedSearch)
	found := false
	for _, s := range searches {
		if s.Name == fixture {
			found = true
			if !strings.Contains(s.Search, "makeresults") {
				t.Errorf("fixture SPL not returned: %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("fixture saved search not in listing (%d entries)", len(searches))
	}

	// run_saved_search executes it under the exact-count contract.
	// The name contains spaces — also exercises path escaping end to end.
	out, err = d.runSavedSearch(ctx, mustJSON(t, map[string]any{"name": fixture}))
	if err != nil {
		t.Fatalf("runSavedSearch: %v", err)
	}
	res := out.(jobResult)
	if res.TotalRows != 2 || len(res.Results) != 2 {
		t.Errorf("total=%d inline=%d, want 2/2", res.TotalRows, len(res.Results))
	}
}

func TestLive_RunSavedSearch_NotFound(t *testing.T) {
	d := liveDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := d.runSavedSearch(ctx, mustJSON(t, map[string]any{"name": "definitely-not-a-real-saved-search"}))
	te := asToolErr(t, err)
	if te.Code != "saved_search_not_found" {
		t.Errorf("code = %q, want saved_search_not_found", te.Code)
	}
}
