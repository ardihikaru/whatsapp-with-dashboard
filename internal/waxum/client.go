// Package waxum is a plain HTTP client for waxum, a Rust REST gateway to
// WhatsApp. internal/wa's waxumBackend (internal/wa/backend_waxum.go) is a
// second Backend implementation built on top of this client, alongside
// whatsmeow's.
//
// This package deliberately does NOT import internal/wa: internal/wa
// imports waxum and translates its typed errors (ErrNotFound etc.) to the
// wa package's own vocabulary (see backend_waxum.go's mapErr) — the reverse
// dependency would be an import cycle, and mixing the two vocabularies here
// would make this client harder to use standalone (e.g. from a future CLI
// or a script) than it needs to be.
//
// Every request/response shape in this file was read from waxum's Rust
// source (src/models/*.rs), not its README, which the phase that added this
// package found to be inaccurate — see each method's doc comment for the
// specific field names that are easy to get wrong.
package waxum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
)

// maxResponseBytes caps how much of a waxum response body this client will
// read, regardless of what the server claims or sends. A malformed or
// malicious response cannot make this client buffer an unbounded amount of
// memory.
const maxResponseBytes = 4 << 20 // 4 MiB

// defaultMaxRateLimitRetries bounds how many times do() retries a 429
// before giving up and returning ErrRateLimited to the caller (production
// default; see WithRetryPolicy for why tests override it). This is
// defense-in-depth, not the primary fix — docker-compose.yml raises
// waxum's own tower_governor RATE_LIMIT_BURST well above what this app's
// pollers need, so retrying here should rarely fire in practice. It exists
// for the gap between "the operator has applied that compose change" and
// "every waxum instance this client ever talks to has generous limits",
// and to smooth over the normal jitter of concurrent pumps sharing one
// IP-keyed bucket.
const defaultMaxRateLimitRetries = 5

// Client is a waxum API client bound to one base URL and one bearer token.
// It is safe for concurrent use (http.Client is).
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	maxRateLimitRetries int
	// retryDelay computes how long to wait before retrying a 429. attempt
	// is 0-based (0 = wait before the first retry); retryAfterHeader is
	// waxum's own Retry-After value, honoured verbatim when present and
	// parseable (RFC 9110 allows either delta-seconds or an HTTP-date).
	retryDelay func(attempt int, retryAfterHeader string) time.Duration

	// limiter is this Client's own outgoing-request throttle — see
	// ratelimit.go's doc comment for why it exists and how it is sized.
	limiter *clientBucket
}

// Option configures optional Client behaviour beyond New's required
// baseURL/token/timeout. Additive and variadic so New's existing call
// sites (cmd/app/main.go, and every test that doesn't care about retry
// timing) need no change.
type Option func(*Client)

// WithRetryPolicy overrides do()'s default 429 retry policy. maxRetries is
// the number of retries AFTER the first attempt (so maxRetries+1 total
// tries before do() gives up and returns ErrRateLimited); delay computes
// the wait before each retry. Production code never calls this — New's
// default (defaultMaxRateLimitRetries, defaultRetryDelay) is what ships.
// Tests use it to replace real wall-clock backoff with a millisecond-scale
// delay, the same way waxumFakeOpts/WaxumConfig already shrink poll
// intervals for tests rather than sleeping seconds per test — see
// TestPairingSurvivesATransientRateLimit.
func WithRetryPolicy(maxRetries int, delay func(attempt int, retryAfterHeader string) time.Duration) Option {
	return func(c *Client) {
		c.maxRateLimitRetries = maxRetries
		c.retryDelay = delay
	}
}

// WithClientRateLimit overrides do()'s default outgoing-request throttle
// (ratelimit.go's defaultClientRate/defaultClientBurst). Production code
// never calls this. Tests use it to raise the limit far above anything a
// test could plausibly burst — the throttle's OWN correctness has a
// dedicated test (TestClientThrottlesRequestsAcrossASharedBucket); every
// other test in this package and internal/wa is exercising unrelated
// behaviour and would otherwise be slowed (or made flaky under `go test
// -race`'s extra scheduling latency) by a limit sized for production
// traffic, not a tight polling loop with a 2ms interval.
func WithClientRateLimit(rate float64, burst int) Option {
	return func(c *Client) {
		c.limiter = newClientBucket(rate, burst)
	}
}

// New builds a Client. baseURL is the scheme+host waxum listens on (e.g.
// "http://127.0.0.1:9000") with any trailing slash trimmed; every method
// below appends its own "/api/v1/..." path (Health is the one exception —
// see its doc comment).
func New(baseURL, token string, timeout time.Duration, opts ...Option) *Client {
	c := &Client{
		baseURL:             strings.TrimSuffix(baseURL, "/"),
		token:               token,
		http:                &http.Client{Timeout: timeout},
		maxRateLimitRetries: defaultMaxRateLimitRetries,
		retryDelay:          defaultRetryDelay,
		limiter:             newClientBucket(defaultClientRate, defaultClientBurst),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// defaultRetryDelay honours waxum's Retry-After header when it sends one
// (delta-seconds per RFC 9110 §10.2.3 — the only form waxum is expected to
// send, but an HTTP-date is accepted too since the RFC allows both);
// otherwise it falls back to the shared backoff+jitter schedule (see
// internal/backoff's doc comment for why this and
// internal/wa/sender_limits.go's send-retry loop share one schedule
// instead of two).
func defaultRetryDelay(attempt int, retryAfterHeader string) time.Duration {
	if d, ok := parseRetryAfter(retryAfterHeader); ok {
		return d
	}
	return backoff.Delay(attempt) + backoff.Jitter()
}

// parseRetryAfter parses an HTTP Retry-After header value. ok is false for
// an empty, malformed, or negative value — callers fall back to their own
// schedule in that case.
func parseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(header); err == nil {
		if d := time.Until(when); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// do sends a request and, on a 2xx response, decodes the body into out
// (skipped when out is nil or the body is empty). authenticate controls
// whether the Authorization header is sent — false only for Health, which
// waxum does not require (or accept as a signal of anything) a token for.
//
// A 429 is retried in place, up to c.maxRateLimitRetries times, with
// c.retryDelay between attempts — see backend_waxum.go's mapErr and
// pairPump doc comments for why: waxum's tower_governor rate limiter
// defaults to a burst of ~3 per peer IP, this app's pairing pollers alone
// can burst past that on a single tick, and a bare 429 used to be mapped
// straight onto ErrQueueFull (a wholly different, misleading condition)
// and aborted pairing outright. Retrying here means most callers never see
// a 429 at all; the caller's own context (a deadline for the pairing pump,
// none for a one-shot send) is still what bounds the total wait — see the
// ctx.Done() case below, which returns immediately rather than sleeping
// past a caller-imposed deadline.
func (c *Client) do(ctx context.Context, method, path string, reqBody, out any, authenticate bool) error {
	var encoded []byte
	if reqBody != nil {
		var err error
		encoded, err = json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("waxum: encoding request body for %s %s: %w", method, path, err)
		}
	}

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

		var body io.Reader
		if encoded != nil {
			body = bytes.NewReader(encoded) // fresh reader per attempt — a *bytes.Reader is spent after one read
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
		if err != nil {
			return fmt.Errorf("waxum: building request for %s %s: %w", method, path, err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authenticate {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("waxum: %s %s: %w", method, path, err)
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		retryAfterHeader := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("waxum: reading response body for %s %s: %w", method, path, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.maxRateLimitRetries {
			lastErr = errorFromStatus(resp.StatusCode, respBody)
			select {
			case <-time.After(c.retryDelay(attempt, retryAfterHeader)):
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

func sessionPath(id string, suffix string) string {
	return "/api/v1/sessions/" + id + suffix
}

// CreateSession provisions (or, with reuse=true, reattaches to) a waxum
// session. id is CLIENT-SUPPLIED and passed verbatim — waxum's own id
// generation is never used, so wa_sessions.id round-trips through waxum with
// no separate mapping table to keep in sync.
func (c *Client) CreateSession(ctx context.Context, id, name string, reuse bool) error {
	body := map[string]any{"id": id, "name": name, "reuse": reuse}
	return c.do(ctx, http.MethodPost, "/api/v1/sessions", body, nil, true)
}

// Connect starts (or resumes) the session's WhatsApp connection.
func (c *Client) Connect(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, sessionPath(id, "/connect"), nil, nil, true)
}

// Disconnect drops the session's live connection without deleting it.
func (c *Client) Disconnect(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, sessionPath(id, "/disconnect"), nil, nil, true)
}

// DeleteSession removes the session. A 404 means it is ALREADY gone and is
// treated as success, not an error — a caller deleting a session it already
// knows is gone (e.g. a retry, or a reconcile pass) should not have to
// special-case ErrNotFound itself.
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, sessionPath(id, ""), nil, nil, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Status fetches the session's current connection state.
func (c *Client) Status(ctx context.Context, id string) (*Status, error) {
	var out Status
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/status"), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// QR fetches the current QR pairing payloads. QRResult.QRCodes are RAW
// payload strings ("2@…"), not rendered images — encoding them as a
// scannable QR code is left to the caller.
func (c *Client) QR(ctx context.Context, id string) (*QRResult, error) {
	var out QRResult
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/qr"), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// PairCode requests a phone-number pairing code.
func (c *Client) PairCode(ctx context.Context, id, phone string, notify bool) (*PairCodeResult, error) {
	body := map[string]any{"phone_number": phone, "show_push_notification": notify}
	var out PairCodeResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/pair"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendText sends a plain-text message.
func (c *Client) SendText(ctx context.Context, id, to, text string) (*MessageResult, error) {
	body := map[string]string{"to": to, "text": text}
	var out MessageResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/messages/text"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMedia sends one of waxum's four media-message kinds. The URL form
// only — waxum also accepts base64-inlined media, which this method does
// NOT implement (scope of this phase).
//
// The request body's shape is the easy part to get wrong: the value under
// the kind's own key (e.g. "image") is an OBJECT ({"url":"https://…"}), not
// the URL string directly. Getting this wrong produces a 422 from waxum,
// which is exactly the class of bug
// TestSendImageWrapsTheURLInAnObjectUnderTheImageKey exists to catch before
// a real request ever goes out.
func (c *Client) SendMedia(ctx context.Context, id string, kind MediaKind, to, url, caption string) (*MessageResult, error) {
	switch kind {
	case MediaImage, MediaVideo, MediaAudio, MediaDocument:
	default:
		return nil, fmt.Errorf("waxum: unknown media kind %q", kind)
	}
	body := map[string]any{
		"to":         to,
		string(kind): map[string]string{"url": url},
	}
	if caption != "" {
		body["caption"] = caption
	}
	var out MessageResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/messages/"+string(kind)), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// Groups lists the session's joined WhatsApp groups.
//
// waxum's wire field is "subject", NOT "name" — Group.Name is mapped from
// it here so nothing downstream has to know that. Group.ParticipantCount is
// likewise the LENGTH of the "participants" array, which waxum sends as a
// full array of objects, not a precomputed count field.
// TestGroupsMapsSubjectToNameAndCountsParticipantsFromTheArrayLength is the
// regression test for both of those.
func (c *Client) Groups(ctx context.Context, id string) ([]Group, error) {
	var resp groupsResponse
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/groups"), nil, &resp, true); err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(resp.Groups))
	for _, g := range resp.Groups {
		out = append(out, Group{
			JID:              g.JID,
			Name:             g.Subject,
			ParticipantCount: len(g.Participants),
			AddressingMode:   g.AddressingMode,
		})
	}
	return out, nil
}

// RegisterWebhook subscribes a URL to receive the named events for this
// session.
func (c *Client) RegisterWebhook(ctx context.Context, id, url, secret string, events []string) error {
	body := map[string]any{"url": url, "events": events, "secret": secret}
	return c.do(ctx, http.MethodPost, sessionPath(id, "/webhooks"), body, nil, true)
}

// Health checks waxum's own liveness. Unlike every other method here, this
// hits "/health" (no "/api/v1" prefix) and is UNAUTHENTICATED — no
// Authorization header is sent, matching waxum's own health-check contract.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil, false)
}
