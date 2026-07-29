// Package client provides a Splunk REST API client.
//
// Ported from nlink-jp/splunk-cli internal/client, adapted for MCP use:
// logging goes through slog (never stdout — that carries JSON-RPC), results
// are returned as raw rows for the tool layer to shape, and job creation
// supports a TTL override so results outlive the default dispatch window.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/config"
	splpkg "github.com/nlink-jp/splunk-mcp/internal/spl"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	defaultOwner       = "nobody"
	// maxResultsPerPage caps a single results request; Splunk's limits.conf
	// bounds per-request rows, so full retrieval pages through at this size.
	maxResultsPerPage = 50_000
)

// defaultPollInterval is how often WaitForJob checks job status.
// A Client field so tests can shorten it.
const defaultPollInterval = 2 * time.Second

// ErrJobNotFound is returned when a SID does not resolve to a job.
var ErrJobNotFound = errors.New("job not found")

// Client is a Splunk REST API client.
type Client struct {
	http   *http.Client
	cfg    *config.Config
	logger *slog.Logger

	// PollInterval controls WaitForJob's status-check cadence.
	PollInterval time.Duration
}

// New creates a new Client. If cfg.HTTPTimeout is zero, defaultHTTPTimeout is
// used. If cfg.Owner is empty, defaultOwner is used.
func New(cfg *config.Config, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if strings.HasPrefix(cfg.Host, "http://") && cfg.Token != "" {
		logger.Warn("sending Splunk token over unencrypted HTTP; use an https:// endpoint", "host", cfg.Host)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("client: create cookie jar: %w", err)
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = defaultHTTPTimeout
	}
	if cfg.Owner == "" {
		cfg.Owner = defaultOwner
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			Jar:       jar,
		},
		cfg:          cfg,
		logger:       logger,
		PollInterval: defaultPollInterval,
	}, nil
}

func (c *Client) apiURL(segments ...string) (string, error) {
	base, err := url.Parse(c.cfg.Host)
	if err != nil {
		return "", fmt.Errorf("invalid host URL: %w", err)
	}
	var parts []string
	if c.cfg.App != "" {
		parts = append([]string{"servicesNS", c.cfg.Owner, c.cfg.App}, segments...)
	} else {
		parts = append([]string{"services"}, segments...)
	}
	return base.JoinPath(parts...).String(), nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.User != "" && c.cfg.Password != "" {
		req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}
	c.logger.Debug("splunk api call", "method", req.Method, "url", req.URL.Redacted())
	return c.http.Do(req)
}

func checkStatus(resp *http.Response, want int) error {
	if resp.StatusCode == want {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("API error %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

// SplunkMessage is a message returned by the Splunk API in a job status response.
type SplunkMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// JobStatus holds the result of a job status check.
type JobStatus struct {
	SID           string
	IsDone        bool
	DispatchState string
	Messages      []SplunkMessage
	ResultCount   int
}

// FailureMessages returns the text of FATAL/ERROR messages, if any.
func (s JobStatus) FailureMessages() []string {
	var out []string
	for _, m := range s.Messages {
		if strings.EqualFold(m.Type, "FATAL") || strings.EqualFold(m.Type, "ERROR") {
			out = append(out, m.Text)
		}
	}
	return out
}

// StartSearch initiates an asynchronous Splunk search (exec_mode=normal is
// the endpoint default) and returns the SID. If cfg.JobTTL is non-zero it is
// sent as the job's keep-alive timeout so results stay retrievable after
// completion.
func (c *Client) StartSearch(ctx context.Context, spl, earliest, latest string) (string, error) {
	endpoint, err := c.apiURL("search", "jobs")
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("search", splpkg.Wrap(spl, c.cfg.Prepend))
	if earliest != "" {
		form.Set("earliest_time", earliest)
	}
	if latest != "" {
		form.Set("latest_time", latest)
	}
	if c.cfg.JobTTL > 0 {
		// "timeout" is the number of seconds the job is kept after processing
		// stops (the job TTL), not a request timeout.
		form.Set("timeout", strconv.Itoa(int(c.cfg.JobTTL.Seconds())))
	}
	form.Set("output_mode", "json")

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

	if err := checkStatus(resp, http.StatusCreated); err != nil {
		return "", err
	}

	var job struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return "", fmt.Errorf("decode start-search response: %w", err)
	}
	return job.SID, nil
}

// GetJobStatus returns the current status of a search job.
// Returns ErrJobNotFound (possibly wrapped) when the SID does not resolve.
func (c *Client) GetJobStatus(ctx context.Context, sid string) (JobStatus, error) {
	endpoint, err := c.apiURL("search", "jobs", sid)
	if err != nil {
		return JobStatus{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return JobStatus{}, err
	}
	q := req.URL.Query()
	q.Set("output_mode", "json")
	req.URL.RawQuery = q.Encode()

	resp, err := c.do(req)
	if err != nil {
		return JobStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return JobStatus{}, fmt.Errorf("%w: %s", ErrJobNotFound, sid)
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return JobStatus{}, err
	}

	var raw struct {
		Entry []struct {
			Content struct {
				IsDone        bool            `json:"isDone"`
				DispatchState string          `json:"dispatchState"`
				Messages      []SplunkMessage `json:"messages"`
				ResultCount   int             `json:"resultCount"`
			} `json:"content"`
		} `json:"entry"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return JobStatus{}, fmt.Errorf("read job status response: %w", err)
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return JobStatus{}, fmt.Errorf("decode job status: %w (body: %s)", err, body)
	}
	if len(raw.Entry) == 0 {
		return JobStatus{}, fmt.Errorf("%w: %s (empty status entry)", ErrJobNotFound, sid)
	}
	content := raw.Entry[0].Content
	return JobStatus{
		SID:           sid,
		IsDone:        content.IsDone,
		DispatchState: content.DispatchState,
		Messages:      content.Messages,
		ResultCount:   content.ResultCount,
	}, nil
}

// WaitForJob polls until the job is done or ctx is cancelled, and returns the
// final status. A FAILED dispatch state is returned as an error carrying the
// job's FATAL/ERROR messages.
func (c *Client) WaitForJob(ctx context.Context, sid string) (JobStatus, error) {
	interval := c.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return JobStatus{}, ctx.Err()
		case <-ticker.C:
			status, err := c.GetJobStatus(ctx, sid)
			if err != nil {
				return JobStatus{}, err
			}
			if !status.IsDone {
				continue
			}
			if status.DispatchState == "FAILED" {
				msgs := status.FailureMessages()
				if len(msgs) > 0 {
					return status, fmt.Errorf("job %s failed: %s", sid, strings.Join(msgs, "; "))
				}
				return status, fmt.Errorf("job %s failed", sid)
			}
			c.logger.Debug("job complete", "sid", sid, "result_count", status.ResultCount)
			return status, nil
		}
	}
}

// FetchResults fetches up to count result rows starting at offset, paging as
// needed. count <= 0 means "all remaining rows" given total (the job's
// resultCount from a prior status call). Rows are returned raw so the caller
// decides between inline JSON and file mediation.
func (c *Client) FetchResults(ctx context.Context, sid string, offset, count, total int) ([]json.RawMessage, error) {
	if offset < 0 {
		offset = 0
	}
	remaining := total - offset
	if remaining < 0 {
		remaining = 0
	}
	if count <= 0 || count > remaining {
		count = remaining
	}

	all := make([]json.RawMessage, 0, count)
	for fetched := 0; fetched < count; {
		pageSize := min(count-fetched, maxResultsPerPage)
		page, err := c.fetchResultsPage(ctx, sid, offset+fetched, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 {
			// Server returned fewer rows than the status promised; stop
			// rather than loop forever.
			break
		}
		fetched += len(page)
	}
	return all, nil
}

// fetchResultsPage fetches one page of results. The response body is closed
// before returning so callers do not need to manage it.
func (c *Client) fetchResultsPage(ctx context.Context, sid string, offset, count int) ([]json.RawMessage, error) {
	endpoint, err := c.apiURL("search", "jobs", sid, "results")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("output_mode", "json")
	q.Set("offset", strconv.Itoa(offset))
	q.Set("count", strconv.Itoa(count))
	req.URL.RawQuery = q.Encode()

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var page struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode results page: %w", err)
	}
	return page.Results, nil
}

// CancelSearch cancels a running job.
func (c *Client) CancelSearch(ctx context.Context, sid string) error {
	endpoint, err := c.apiURL("search", "jobs", sid, "control")
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("action=cancel"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: %s", ErrJobNotFound, sid)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel job %s: %s: %s", sid, resp.Status, body)
	}
	c.logger.Debug("job cancelled", "sid", sid)
	return nil
}
