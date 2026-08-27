package waxum

import (
	"context"
	"net/http"
)

// WebhookConfigWithID is a registered webhook subscription, as returned by
// RegisterWebhook and ListWebhooks.
//
// Secret is an HMAC signing key waxum uses to sign delivered payloads. It
// must NEVER be logged or rendered back to an operator verbatim — treat it
// exactly like the WAXUM_TOKEN/SUPERADMIN_TOKEN credential documented in
// CLAUDE.md.
type WebhookConfigWithID struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Secret  *string  `json:"secret"`
	Enabled bool     `json:"enabled"`
}

// WebhookDLQEntry is one dead-lettered webhook delivery: a payload waxum
// gave up retrying after WEBHOOK_RETRY_MAX_ATTEMPTS attempts.
type WebhookDLQEntry struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	WebhookURL string `json:"webhook_url"`
	Event      string `json:"event"`
	Payload    string `json:"payload"`
	LastError  string `json:"last_error"`
	Attempts   int    `json:"attempts"`
	FailedAt   int64  `json:"failed_at"`
}

// RegisterWebhook subscribes a URL to receive the named events for this
// session, returning the created config. waxum's SSRF guard (see SendMedia's
// doc comment) applies here too: a private/RFC1918 callback URL is refused
// with a 400, not silently accepted — surface that to the operator plainly
// rather than as a generic failure, since it is the single most likely
// mistake here (most operator-run receivers ARE on a private network).
func (c *Client) RegisterWebhook(ctx context.Context, id, url, secret string, events []string) (*WebhookConfigWithID, error) {
	body := map[string]any{"url": url, "events": events, "secret": secret}
	var out WebhookConfigWithID
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/webhooks"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListWebhooks lists every webhook registered for this session.
func (c *Client) ListWebhooks(ctx context.Context, id string) ([]WebhookConfigWithID, error) {
	var out struct {
		Webhooks []WebhookConfigWithID `json:"webhooks"`
		Count    int                   `json:"count"`
	}
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/webhooks"), nil, &out, true); err != nil {
		return nil, err
	}
	return out.Webhooks, nil
}

// UnregisterWebhook removes a webhook subscription.
func (c *Client) UnregisterWebhook(ctx context.Context, id, webhookID string) error {
	return c.do(ctx, http.MethodDelete, sessionPath(id, "/webhooks/"+webhookID), nil, nil, true)
}

// ReenableWebhook re-enables a webhook whose delivery circuit waxum had
// opened (disabled) after repeated failures.
func (c *Client) ReenableWebhook(ctx context.Context, id, webhookID string) error {
	return c.do(ctx, http.MethodPost, sessionPath(id, "/webhooks/"+webhookID+"/enable"), nil, nil, true)
}

// WebhookDLQ lists this session's dead-lettered webhook deliveries.
func (c *Client) WebhookDLQ(ctx context.Context, id string) ([]WebhookDLQEntry, error) {
	var out struct {
		Entries []WebhookDLQEntry `json:"entries"`
		Count   int               `json:"count"`
	}
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/webhooks/dlq"), nil, &out, true); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// ReplayWebhookDLQ retries one dead-lettered delivery by entry id.
func (c *Client) ReplayWebhookDLQ(ctx context.Context, id, entryID string) error {
	return c.do(ctx, http.MethodPost, sessionPath(id, "/webhooks/dlq/"+entryID+"/replay"), nil, nil, true)
}

// ReenableAllWebhooks re-enables every open webhook circuit across every
// session on this waxum instance — a FLEET-level operation, NOT scoped
// under /sessions/{id} like everything else in this file.
func (c *Client) ReenableAllWebhooks(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/v1/webhooks/reenable-all", nil, nil, true)
}
