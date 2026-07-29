package client

// Discovery endpoints: index and saved-search enumeration, and saved-search
// dispatch. These are plain REST entity listings (no search job) except
// DispatchSavedSearch, which creates a job like StartSearch does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrSavedSearchNotFound is returned when a saved-search name does not resolve.
var ErrSavedSearchNotFound = errors.New("saved search not found")

// IndexInfo describes one event index.
type IndexInfo struct {
	Name       string `json:"name"`
	EventCount int64  `json:"event_count"`
	// MinTime/MaxTime are the index's event time bounds as reported by
	// Splunk (ISO-8601 strings; empty for empty indexes).
	MinTime  string `json:"earliest_time,omitempty"`
	MaxTime  string `json:"latest_time,omitempty"`
	Disabled bool   `json:"disabled"`
}

// SavedSearch describes one saved search.
type SavedSearch struct {
	Name        string `json:"name"`
	Search      string `json:"search"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled"`
	IsScheduled bool   `json:"is_scheduled"`
	// CronSchedule is set only for scheduled searches.
	CronSchedule string `json:"cron_schedule,omitempty"`
}

// restEntry is one entry of a Splunk entity-listing response.
type restEntry struct {
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`
}

// listEntries GETs an entity collection with count=0 (no pagination cap) and
// returns its entries.
func (c *Client) listEntries(ctx context.Context, segments ...string) ([]restEntry, error) {
	endpoint, err := c.apiURL(segments...)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("output_mode", "json")
	q.Set("count", "0")
	req.URL.RawQuery = q.Encode()

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var raw struct {
		Entry []restEntry `json:"entry"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode entity listing: %w", err)
	}
	return raw.Entry, nil
}

// ListIndexes returns the event indexes visible to the authenticated role.
func (c *Client) ListIndexes(ctx context.Context) ([]IndexInfo, error) {
	entries, err := c.listEntries(ctx, "data", "indexes")
	if err != nil {
		return nil, err
	}
	out := make([]IndexInfo, 0, len(entries))
	for _, e := range entries {
		var content struct {
			TotalEventCount json.Number `json:"totalEventCount"`
			MinTime         string      `json:"minTime"`
			MaxTime         string      `json:"maxTime"`
			Disabled        bool        `json:"disabled"`
		}
		if err := json.Unmarshal(e.Content, &content); err != nil {
			return nil, fmt.Errorf("decode index %q: %w", e.Name, err)
		}
		count, _ := strconv.ParseInt(content.TotalEventCount.String(), 10, 64)
		out = append(out, IndexInfo{
			Name:       e.Name,
			EventCount: count,
			MinTime:    content.MinTime,
			MaxTime:    content.MaxTime,
			Disabled:   content.Disabled,
		})
	}
	return out, nil
}

// ListSavedSearches returns the saved searches visible in the configured
// app/owner namespace.
func (c *Client) ListSavedSearches(ctx context.Context) ([]SavedSearch, error) {
	entries, err := c.listEntries(ctx, "saved", "searches")
	if err != nil {
		return nil, err
	}
	out := make([]SavedSearch, 0, len(entries))
	for _, e := range entries {
		var content struct {
			Search       string `json:"search"`
			Description  string `json:"description"`
			Disabled     bool   `json:"disabled"`
			IsScheduled  bool   `json:"is_scheduled"`
			CronSchedule string `json:"cron_schedule"`
		}
		if err := json.Unmarshal(e.Content, &content); err != nil {
			return nil, fmt.Errorf("decode saved search %q: %w", e.Name, err)
		}
		out = append(out, SavedSearch{
			Name:         e.Name,
			Search:       content.Search,
			Description:  content.Description,
			Disabled:     content.Disabled,
			IsScheduled:  content.IsScheduled,
			CronSchedule: content.CronSchedule,
		})
	}
	return out, nil
}

// DispatchSavedSearch runs a saved search as a job and returns the SID.
// earliest/latest override the saved search's dispatch window when non-empty.
// Returns ErrSavedSearchNotFound (possibly wrapped) for an unknown name.
func (c *Client) DispatchSavedSearch(ctx context.Context, name, earliest, latest string) (string, error) {
	// url.JoinPath (via apiURL) treats "/" inside an element as a path
	// separator, so the name is escaped explicitly to stay one segment.
	base, err := c.apiURL("saved", "searches")
	if err != nil {
		return "", err
	}
	endpoint := base + "/" + url.PathEscape(name) + "/dispatch"

	form := url.Values{}
	form.Set("output_mode", "json")
	// Never fire the saved search's alert actions from an analysis tool.
	form.Set("trigger_actions", "0")
	if earliest != "" {
		form.Set("dispatch.earliest_time", earliest)
	}
	if latest != "" {
		form.Set("dispatch.latest_time", latest)
	}
	if c.cfg.JobTTL > 0 {
		form.Set("dispatch.ttl", strconv.Itoa(int(c.cfg.JobTTL.Seconds())))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: %s", ErrSavedSearchNotFound, name)
	}
	if err := checkStatus(resp, http.StatusCreated); err != nil {
		return "", err
	}

	var job struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return "", fmt.Errorf("decode dispatch response: %w", err)
	}
	return job.SID, nil
}
