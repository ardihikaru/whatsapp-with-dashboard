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
	"strings"
	"time"
)

// maxResponseBytes caps how much of a waxum response body this client will
// read, regardless of what the server claims or sends. A malformed or
// malicious response cannot make this client buffer an unbounded amount of
// memory.
const maxResponseBytes = 4 << 20 // 4 MiB

// Client is a waxum API client bound to one base URL and one bearer token.
// It is safe for concurrent use (http.Client is).
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client. baseURL is the scheme+host waxum listens on (e.g.
// "http://127.0.0.1:9000") with any trailing slash trimmed; every method
// below appends its own "/api/v1/..." path (Health is the one exception —
// see its doc comment).
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

// do sends one request and, on a 2xx response, decodes the body into out
// (skipped when out is nil or the body is empty). authenticate controls
// whether the Authorization header is sent — false only for Health, which
// waxum does not require (or accept as a signal of anything) a token for.
func (c *Client) do(ctx context.Context, method, path string, reqBody, out any, authenticate bool) error {
	var body io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("waxum: encoding request body for %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
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
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("waxum: reading response body for %s %s: %w", method, path, err)
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
