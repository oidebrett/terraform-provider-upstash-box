// Package client is a small REST client for the Upstash Box API
// (https://us-east-1.box.upstash.com/v2/box). The endpoints and body shapes
// mirror the official @upstash/box SDK (0.7.x).
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const DefaultBaseURL = "https://us-east-1.box.upstash.com"

// ErrNotFound is returned when the API answers 404 for a box or public URL.
var ErrNotFound = errors.New("upstash box: not found")

// APIError carries the HTTP status and the API's {"error": "..."} message.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("upstash box API: HTTP %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// PollInterval is how often WaitReady polls a creating box.
	PollInterval time.Duration
}

func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// Exec blocks until the command finishes; per-call timeouts come from ctx.
		HTTPClient:   &http.Client{},
		PollInterval: 2 * time.Second,
	}
}

// Box is the subset of the box record the provider uses.
type Box struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Runtime             string   `json:"runtime"`
	Size                string   `json:"size"`
	Status              string   `json:"status"`
	KeepAlive           bool     `json:"keep_alive"`
	Labels              []string `json:"labels"`
	StorageGB           int64    `json:"storage_gb"`
	TotalCPUNs          int64    `json:"total_cpu_ns"`
	TotalComputeCostUSD float64  `json:"total_compute_cost_usd"`
	LastActivityAt      int64    `json:"last_activity_at"`
	CreatedAt           int64    `json:"created_at"`
}

type CreateBoxRequest struct {
	Name          string            `json:"name,omitempty"`
	Labels        []string          `json:"labels,omitempty"`
	Size          string            `json:"size,omitempty"`
	Runtime       string            `json:"runtime,omitempty"`
	KeepAlive     bool              `json:"keep_alive,omitempty"`
	InitCommand   *string           `json:"init_command,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
	NetworkPolicy *NetworkPolicy    `json:"network_policy,omitempty"`
}

// NetworkPolicy is passed through as the API expects it, e.g.
// {"mode":"allow-all"} or {"mode":"custom","allowed_domains":[...]}.
type NetworkPolicy struct {
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	AllowedCidrs   []string `json:"allowed_cidrs,omitempty"`
	DeniedCidrs    []string `json:"denied_cidrs,omitempty"`
}

type ExecResult struct {
	Output   string `json:"output"`
	Error    string `json:"error"`
	ExitCode int64  `json:"exit_code"`
}

type PublicURL struct {
	URL      string `json:"url"`
	Port     int64  `json:"port"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type PublicURLListItem struct {
	ID          string `json:"id"`
	Port        int64  `json:"port"`
	URL         string `json:"url"`
	BasicAuth   bool   `json:"basic_auth"`
	BearerToken bool   `json:"bearer_token"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Box-Api-Key", c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func (c *Client) CreateBox(ctx context.Context, r CreateBoxRequest) (*Box, error) {
	var b Box
	if err := c.do(ctx, http.MethodPost, "/v2/box", r, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// GetBox returns ErrNotFound for a missing box and also for one the API
// reports as "deleted", so callers can drop it from state either way.
func (c *Client) GetBox(ctx context.Context, id string) (*Box, error) {
	var b Box
	if err := c.do(ctx, http.MethodGet, "/v2/box/"+id, nil, &b); err != nil {
		return nil, err
	}
	if b.Status == "deleted" {
		return nil, ErrNotFound
	}
	return &b, nil
}

func (c *Client) DeleteBox(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/v2/box/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// WaitReady polls until the box leaves "creating". It fails on "error".
func (c *Client) WaitReady(ctx context.Context, id string) (*Box, error) {
	for {
		b, err := c.GetBox(ctx, id)
		if err != nil {
			return nil, err
		}
		switch b.Status {
		case "creating":
		case "error":
			return b, fmt.Errorf("box %s entered status error while creating", id)
		default:
			return b, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for box %s to be ready: %w", id, ctx.Err())
		case <-time.After(c.PollInterval):
		}
	}
}

func (c *Client) Pause(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/box/"+id+"/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/box/"+id+"/resume", nil, nil)
}

func (c *Client) GetInitCommand(ctx context.Context, id string) (string, error) {
	var r struct {
		InitCommand string `json:"init_command"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/box/"+id+"/startup", nil, &r); err != nil {
		return "", err
	}
	return r.InitCommand, nil
}

// SetInitCommand replaces the init command; an empty string clears it.
func (c *Client) SetInitCommand(ctx context.Context, id, cmd string) error {
	return c.do(ctx, http.MethodPut, "/v2/box/"+id+"/startup", map[string]string{"init_command": cmd}, nil)
}

// Exec runs `sh -c script` as boxuser and blocks until it exits.
func (c *Client) Exec(ctx context.Context, id, script string) (*ExecResult, error) {
	body := map[string]any{"command": []string{"sh", "-c", script}}
	var r ExecResult
	if err := c.do(ctx, http.MethodPost, "/v2/box/"+id+"/exec", body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) CreatePublicURL(ctx context.Context, id string, port int64, bearer, basic bool) (*PublicURL, error) {
	body := map[string]any{"port": port}
	if bearer {
		body["bearer_token"] = true
	}
	if basic {
		body["basic_auth"] = true
	}
	var u PublicURL
	if err := c.do(ctx, http.MethodPost, "/v2/box/"+id+"/preview", body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) ListPublicURLs(ctx context.Context, id string) ([]PublicURLListItem, error) {
	var r struct {
		Previews []PublicURLListItem `json:"previews"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/box/"+id+"/preview", nil, &r); err != nil {
		return nil, err
	}
	return r.Previews, nil
}

func (c *Client) DeletePublicURL(ctx context.Context, id string, port int64) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/v2/box/%s/preview/%d", id, port), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// RunResult is the outcome of a detached script run.
type RunResult struct {
	ExitCode int64
	// LogTail is the last part of the combined stdout/stderr log.
	LogTail string
}

// RunDetached uploads script to the box, starts it in the background with
// nohup and polls for its exit code. A long install therefore never depends
// on one HTTP request staying open. The log stays at /tmp/<runID>.log.
func (c *Client) RunDetached(ctx context.Context, boxID, runID, interpreter, script string, poll time.Duration) (*RunResult, error) {
	if interpreter == "" {
		interpreter = "bash"
	}
	base := "/tmp/" + runID
	enc := base64.StdEncoding.EncodeToString([]byte(script))
	// Write the script, then launch it. The launcher only mentions file paths,
	// never a process name, so nothing here can match itself with pgrep/pkill.
	launch := fmt.Sprintf(
		"set -e; echo %s | base64 -d > %s.sh; rm -f %s.rc; "+
			"nohup sh -c '%s %s.sh > %s.log 2>&1; echo $? > %s.rc' > /dev/null 2>&1 &",
		enc, base, base, interpreter, base, base, base)
	res, err := c.Exec(ctx, boxID, launch)
	if err != nil {
		return nil, fmt.Errorf("starting script: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("starting script: exit %d: %s%s", res.ExitCode, res.Output, res.Error)
	}

	for {
		select {
		case <-ctx.Done():
			tail, _ := c.Exec(context.Background(), boxID, "tail -c 4000 "+base+".log")
			msg := ""
			if tail != nil {
				msg = tail.Output
			}
			return nil, fmt.Errorf("script still running when the timeout hit (%w); log tail:\n%s", ctx.Err(), msg)
		case <-time.After(poll):
		}
		st, err := c.Exec(ctx, boxID, "cat "+base+".rc 2>/dev/null || true")
		if err != nil {
			if ctx.Err() != nil {
				continue // reported on the next loop turn
			}
			return nil, fmt.Errorf("polling script: %w", err)
		}
		rc := strings.TrimSpace(st.Output)
		if rc == "" {
			continue
		}
		code, err := strconv.ParseInt(rc, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected exit code file content %q", rc)
		}
		tail, err := c.Exec(ctx, boxID, "tail -c 4000 "+base+".log")
		if err != nil {
			return nil, fmt.Errorf("reading log: %w", err)
		}
		return &RunResult{ExitCode: code, LogTail: tail.Output}, nil
	}
}

// EnsureAwake resumes a paused box and waits until it is no longer paused.
func (c *Client) EnsureAwake(ctx context.Context, id string) error {
	b, err := c.GetBox(ctx, id)
	if err != nil {
		return err
	}
	if b.Status != "paused" {
		return nil
	}
	if err := c.Resume(ctx, id); err != nil {
		return fmt.Errorf("resuming box %s: %w", id, err)
	}
	for {
		b, err := c.GetBox(ctx, id)
		if err != nil {
			return err
		}
		if b.Status != "paused" && b.Status != "creating" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for box %s to resume: %w", id, ctx.Err())
		case <-time.After(c.PollInterval):
		}
	}
}
