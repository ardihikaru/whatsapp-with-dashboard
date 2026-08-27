package wa

import (
	"context"
	"errors"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// This file holds waxumBackend's implementation of WebhookAdmin
// (capabilities.go). Every error crosses mapErr, matching every other
// waxumBackend method in this package. sessionID is a plain string, not
// *ManagedSession — see backend_waxum_ops.go's doc comment for why.

// ListWebhooks satisfies WebhookAdmin.
func (b *waxumBackend) ListWebhooks(ctx context.Context, sessionID string) ([]WebhookInfo, error) {
	hooks, err := b.client.ListWebhooks(ctx, sessionID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]WebhookInfo, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, WebhookInfo{ID: h.ID, URL: h.URL, Events: h.Events, Secret: h.Secret, Enabled: h.Enabled})
	}
	return out, nil
}

// RegisterWebhook satisfies WebhookAdmin. waxum's SSRF guard rejects a
// private/RFC1918 callback URL with a 400 — mapErr turns that into
// ErrRejectedByBackend, which the handler layer is expected to surface to
// the operator plainly, per RegisterWebhook's own doc comment
// (internal/waxum/webhooks.go): "most operator-run receivers ARE on a
// private network" is the single most likely mistake here.
//
// A duplicate URL answers waxum's generic 409 (waxum.ErrConflict), which
// mapErr's generic case maps to ErrNotConnected — the right call for
// Connect/Disconnect's "already connected" 409, but a misleading one here:
// "session not connected" is not what happened. Caught BEFORE mapErr and
// mapped to ErrAlreadyExists instead — see that sentinel's own doc comment
// (errors.go).
func (b *waxumBackend) RegisterWebhook(ctx context.Context, sessionID string, spec WebhookSpec) (WebhookInfo, error) {
	h, err := b.client.RegisterWebhook(ctx, sessionID, spec.URL, spec.Secret, spec.Events)
	if err != nil {
		if errors.Is(err, waxum.ErrConflict) {
			return WebhookInfo{}, ErrAlreadyExists
		}
		return WebhookInfo{}, mapErr(err)
	}
	return WebhookInfo{ID: h.ID, URL: h.URL, Events: h.Events, Secret: h.Secret, Enabled: h.Enabled}, nil
}

// UnregisterWebhook satisfies WebhookAdmin.
func (b *waxumBackend) UnregisterWebhook(ctx context.Context, sessionID, webhookID string) error {
	return mapErr(b.client.UnregisterWebhook(ctx, sessionID, webhookID))
}

// EnableWebhook satisfies WebhookAdmin.
func (b *waxumBackend) EnableWebhook(ctx context.Context, sessionID, webhookID string) error {
	return mapErr(b.client.ReenableWebhook(ctx, sessionID, webhookID))
}

// WebhookDLQ satisfies WebhookAdmin.
func (b *waxumBackend) WebhookDLQ(ctx context.Context, sessionID string) ([]DLQEntry, error) {
	entries, err := b.client.WebhookDLQ(ctx, sessionID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]DLQEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, DLQEntry{
			ID:         e.ID,
			SessionID:  e.SessionID,
			WebhookURL: e.WebhookURL,
			Event:      e.Event,
			Payload:    e.Payload,
			LastError:  e.LastError,
			Attempts:   e.Attempts,
			FailedAt:   e.FailedAt,
		})
	}
	return out, nil
}

// ReplayWebhookDLQEntry satisfies WebhookAdmin.
func (b *waxumBackend) ReplayWebhookDLQEntry(ctx context.Context, sessionID, entryID string) error {
	return mapErr(b.client.ReplayWebhookDLQ(ctx, sessionID, entryID))
}

// ReenableAllWebhooks satisfies WebhookAdmin — the one FLEET-level
// operation in this file, not scoped to a session (see
// waxum.ReenableAllWebhooks's own doc comment).
func (b *waxumBackend) ReenableAllWebhooks(ctx context.Context) error {
	return mapErr(b.client.ReenableAllWebhooks(ctx))
}

// var _ WebhookAdmin guards against a typo'd signature compiling and only
// failing at runtime as a bogus ErrUnsupported.
var _ WebhookAdmin = (*waxumBackend)(nil)
