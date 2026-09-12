package waxum

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// send is the body-agnostic core of every request this client makes: it
// owns the client-wide throttle (ratelimit.go), the 429 retry loop (honouring
// Retry-After, up to c.maxRateLimitRetries), the response-size cap, and
// status-to-error mapping (errorFromStatus). do (below) is a thin JSON
// wrapper around it, and media.go's multipart uploads call it directly with
// a pre-built body — see that file's doc comments for why a SECOND retry
// loop must never be written: a copy would silently not retry 429s, which is
// exactly the bug this extraction exists to make impossible.
//
// body is nil for a request with no body at all (e.g. Connect); a non-nil
// body is replayed via a fresh bytes.NewReader on every attempt, including
// retries — a *bytes.Reader is spent after one read, so reusing it across
// attempts would silently send an empty body on a retry. maxResp overrides
// how many bytes of the response this call will read (DownloadMedia uses a
// larger cap than everything else — see maxMediaResponseBytes in media.go).
func (c *Client) send(ctx context.Context, method, path, contentType string,
	body []byte, out any, authenticate bool, maxResp int64) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		// Client-wide throttle (ratelimit.go) — applies to every attempt,
		// including retries, since each is a real outgoing request. A
		// cancelled/expired ctx here returns immediately rather than
		// waiting out the limiter, same reasoning as the 429-retry
		// ctx.Done() case below.
		if err := c.limiter.wait(ctx); err != nil {
			return fmt.Errorf("waxum: %s %s: %w", method, path, err)
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body) // fresh reader per attempt — a *bytes.Reader is spent after one read
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return fmt.Errorf("waxum: building request for %s %s: %w", method, path, err)
		}
		if body != nil && contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if authenticate {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("waxum: %s %s: %w", method, path, err)
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResp))
		retryAfterHeader := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("waxum: reading response body for %s %s: %w", method, path, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.maxRateLimitRetries {
			lastErr = errorFromStatus(resp.StatusCode, respBody)
			delay := c.retryDelay(attempt, retryAfterHeader)
			// This used to be completely silent — a 429 that retried for
			// hours (see client.go's maxRetryAfter doc comment for the
			// live incident) left no trace anywhere that anything was
			// waiting at all. slog.Warn here is the only place that wait
			// is now visible.
			slog.Warn("waxum: rate limited; retrying", "method", method, "path", path,
				"attempt", attempt+1, "delay", delay, "retry_after_header", retryAfterHeader)
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
			return fmt.Errorf("waxum: decoding response body for %s %s: %w", method, path, err)
		}
		return nil
	}
}
