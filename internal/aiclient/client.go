// Package aiclient is a plain HTTP client for the ai-codex-cli REST service
// (rnd-v2-vpn) — an ASYNC CODING-AGENT JOB RUNNER, not a lightweight
// paraphrase endpoint: a caller submits a job (POST /v1/jobs), polls it
// (GET /v1/jobs/{id}) until it reaches a terminal status, then reads its
// output (GET /v1/jobs/{id}/logs). Confirmed live against
// ~/.config/ai-stack/basic-auth.json and that service's own rest-apis.md
// doc — see the phase plan's Context section for the full story of why this
// shape drives internal/groupbroadcast to generate every message's wording
// at PLANNING time (hours ahead of the actual send), never synchronously at
// send time.
//
// Follows internal/waxum/client.go's house pattern exactly: functional
// options, a bounded response-body read, retry/backoff reusing the shared
// internal/backoff schedule (never a second retry curve), typed sentinel
// errors, and a single construction at boot. This package deliberately does
// NOT import internal/groupbroadcast or internal/selfconvo — generate.go is
// the one policy layer built on top of this pure transport client, mirroring
// how internal/wa/backend_waxum.go builds policy on top of internal/waxum
// while keeping *Client itself transport-only.
package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
)

// maxResponseBytes caps how much of an ai-codex-cli response body this
// client will read, regardless of what the server claims or sends — same
// defense-in-depth reasoning as internal/waxum/client.go's constant of the
// same name.
const maxResponseBytes = 4 << 20 // 4 MiB

// defaultMaxRetries bounds how many times do() retries a 429/503 before
// giving up — mirrors internal/waxum/client.go's
// defaultMaxRateLimitRetries.
const defaultMaxRetries = 5

// Job statuses ai-codex-cli reports on GET /v1/jobs/{id}.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobCompleted = "completed"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// IsJobTerminal reports whether status is one a job never leaves once
// reached.
func IsJobTerminal(status string) bool {
	switch status {
	case JobCompleted, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// JobRequest is the body of POST /v1/jobs.
type JobRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model,omitempty"`
}

// Job is GET /v1/jobs/{id}, decoded verbatim.
type Job struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// JobLogs is GET /v1/jobs/{id}/logs, decoded verbatim. Stdout carries the
// job's actual text output — extractText (generate.go) is what turns this
// into the one-line WhatsApp message GenerateMessage returns.
type JobLogs struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr,omitempty"`
}

// Typed errors this package maps well-known HTTP statuses to. Anything else
// non-2xx becomes an *APIError.
var (
	ErrNotFound    = errors.New("aiclient: job not found")
	ErrRateLimited = errors.New("aiclient: rate limited")
)

// APIError is returned for any non-2xx response that isn't one of the typed
// sentinels above.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("aiclient: unexpected status %d: %s", e.StatusCode, e.Body)
}

func errorFromStatus(statusCode int, body []byte) error {
	switch statusCode {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return &APIError{StatusCode: statusCode, Body: string(body)}
	}
}

// Client is an ai-codex-cli API client bound to one base URL and one HTTP
// Basic credential pair. Safe for concurrent use (http.Client is).
type Client struct {
	baseURL string
	loginID string
	secret  string
	http    *http.Client

	maxRetries int
	// retryDelay computes how long to wait before retrying a 429/503.
	// attempt is 0-based. Mirrors internal/waxum/client.go's retryDelay
	// field exactly, minus the Retry-After header honouring — ai-codex-cli
	// is not documented to send one, so every retry here uses the shared
	// backoff schedule directly.
	retryDelay func(attempt int) time.Duration
}

// Option configures optional Client behaviour beyond New's required
// baseURL/loginID/secret/timeout.
type Option func(*Client)

// WithRetryPolicy overrides do()'s default 429/503 retry policy. Production
// code never calls this; tests use it to replace real wall-clock backoff
// with a millisecond-scale delay — same convention as
// internal/waxum.WithRetryPolicy.
func WithRetryPolicy(maxRetries int, delay func(attempt int) time.Duration) Option {
	return func(c *Client) {
		c.maxRetries = maxRetries
		c.retryDelay = delay
	}
}

// New builds a Client. baseURL is the scheme+host ai-codex-cli listens on,
// trailing slash trimmed; every method below appends its own "/v1/..."
// path.
func New(baseURL, loginID, secretKey string, timeout time.Duration, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		loginID:    loginID,
		secret:     secretKey,
		http:       &http.Client{Timeout: timeout},
		maxRetries: defaultMaxRetries,
		retryDelay: defaultRetryDelay,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// defaultRetryDelay falls back to the shared backoff+jitter schedule — see
// internal/backoff's doc comment for why every retrying caller in this
// codebase shares one curve instead of inventing its own.
func defaultRetryDelay(attempt int) time.Duration {
	return backoff.Delay(attempt) + backoff.Jitter()
}

func (c *Client) do(ctx context.Context, method, path string, reqBody, out any) error {
	var encoded []byte
	if reqBody != nil {
		var err error
		encoded, err = json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("aiclient: encoding request body for %s %s: %w", method, path, err)
		}
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if encoded != nil {
			reader = bytes.NewReader(encoded) // fresh reader per attempt
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return fmt.Errorf("aiclient: building request for %s %s: %w", method, path, err)
		}
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.SetBasicAuth(c.loginID, c.secret)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("aiclient: %s %s: %w", method, path, err)
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("aiclient: reading response body for %s %s: %w", method, path, err)
		}

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) &&
			attempt < c.maxRetries {
			lastErr = errorFromStatus(resp.StatusCode, respBody)
			delay := c.retryDelay(attempt)
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return lastErr
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return errorFromStatus(resp.StatusCode, respBody)
		}
		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("aiclient: decoding response body for %s %s: %w", method, path, err)
		}
		return nil
	}
}

// SubmitJob submits req and returns the new job's id. 202 is treated the
// same as any other 2xx — errorFromStatus only special-cases non-2xx.
func (c *Client) SubmitJob(ctx context.Context, req JobRequest) (string, error) {
	var out Job
	if err := c.do(ctx, http.MethodPost, "/v1/jobs", req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// GetJob fetches jobID's current status.
func (c *Client) GetJob(ctx context.Context, jobID string) (Job, error) {
	var out Job
	if err := c.do(ctx, http.MethodGet, "/v1/jobs/"+jobID, nil, &out); err != nil {
		return Job{}, err
	}
	return out, nil
}

// GetLogs fetches jobID's output — the caller's only way to read what a
// completed job actually produced.
func (c *Client) GetLogs(ctx context.Context, jobID string) (JobLogs, error) {
	var out JobLogs
	if err := c.do(ctx, http.MethodGet, "/v1/jobs/"+jobID+"/logs", nil, &out); err != nil {
		return JobLogs{}, err
	}
	return out, nil
}

// CancelJob asks ai-codex-cli to stop jobID. Not used by GenerateMessage
// today (a timed-out job is simply abandoned, not cancelled — see that
// function's doc comment for why), but part of the transport surface the
// phase plan names.
func (c *Client) CancelJob(ctx context.Context, jobID string) error {
	return c.do(ctx, http.MethodPost, "/v1/jobs/"+jobID+"/cancel", nil, nil)
}
