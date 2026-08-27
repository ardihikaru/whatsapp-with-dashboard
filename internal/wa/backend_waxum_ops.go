package wa

import (
	"context"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// This file holds waxumBackend's implementation of the GatewayHealth and
// SessionOps capability interfaces (capabilities.go) — every waxum
// operation that has no whatsmeow analogue at all: a fleet health probe,
// pause/resume, auto-reconnect and history-sync toggles, appstate resync,
// spam reporting, and the trust-conversation-token family. Every error
// crosses mapErr, matching every other waxumBackend method in this
// package.
//
// sessionID is a plain string everywhere here, not *ManagedSession — these
// are called from HTTP handlers keyed off the URL path id, the same reason
// MediaStore's methods take one (capabilities.go's doc comment).

// Health satisfies GatewayHealth.
func (b *waxumBackend) Health(ctx context.Context) error {
	return mapErr(b.client.Health(ctx))
}

// IsPaused satisfies SessionOps by reading waxum's own GET .../status
// payload rather than Pause/Resume, neither of which is safe to call just
// to answer a read — see IsPaused's doc comment (capabilities.go).
func (b *waxumBackend) IsPaused(ctx context.Context, sessionID string) (bool, error) {
	st, err := b.client.Status(ctx, sessionID)
	if err != nil {
		return false, mapErr(err)
	}
	return st.Paused, nil
}

// Pause satisfies SessionOps.
func (b *waxumBackend) Pause(ctx context.Context, sessionID string) (bool, error) {
	paused, err := b.client.Pause(ctx, sessionID)
	if err != nil {
		return false, mapErr(err)
	}
	return paused, nil
}

// Resume satisfies SessionOps.
func (b *waxumBackend) Resume(ctx context.Context, sessionID string) (bool, error) {
	paused, err := b.client.Resume(ctx, sessionID)
	if err != nil {
		return false, mapErr(err)
	}
	return paused, nil
}

// AutoReconnect satisfies SessionOps.
func (b *waxumBackend) AutoReconnect(ctx context.Context, sessionID string) (AutoReconnectState, error) {
	ar, err := b.client.AutoReconnect(ctx, sessionID)
	if err != nil {
		return AutoReconnectState{}, mapErr(err)
	}
	return AutoReconnectState{Enabled: ar.Enabled, ErrorCount: ar.ErrorCount}, nil
}

// SetAutoReconnect satisfies SessionOps.
func (b *waxumBackend) SetAutoReconnect(ctx context.Context, sessionID string, enabled bool) (AutoReconnectState, error) {
	ar, err := b.client.SetAutoReconnect(ctx, sessionID, enabled)
	if err != nil {
		return AutoReconnectState{}, mapErr(err)
	}
	return AutoReconnectState{Enabled: ar.Enabled, ErrorCount: ar.ErrorCount}, nil
}

// HistorySync satisfies SessionOps.
func (b *waxumBackend) HistorySync(ctx context.Context, sessionID string) (bool, error) {
	skip, err := b.client.HistorySync(ctx, sessionID)
	if err != nil {
		return false, mapErr(err)
	}
	return skip, nil
}

// SetHistorySync satisfies SessionOps.
func (b *waxumBackend) SetHistorySync(ctx context.Context, sessionID string, skip bool) (bool, error) {
	got, err := b.client.SetHistorySync(ctx, sessionID, skip)
	if err != nil {
		return false, mapErr(err)
	}
	return got, nil
}

// AppStateResync satisfies SessionOps.
func (b *waxumBackend) AppStateResync(ctx context.Context, sessionID string, collections []string, mode string) (AppStateResyncResult, error) {
	res, err := b.client.AppStateResync(ctx, sessionID, collections, mode)
	if err != nil {
		return AppStateResyncResult{}, mapErr(err)
	}
	return AppStateResyncResult{
		Synced:    res.Synced,
		Fatal:     res.Fatal,
		Retryable: res.Retryable,
		Skipped:   res.Skipped,
		AllSynced: res.AllSynced,
	}, nil
}

// ReportSpam satisfies SessionOps. Forwards every field of SpamReport
// verbatim — see waxum.SpamReport's doc comment for why MessageTimestamp
// in particular must never be dropped.
func (b *waxumBackend) ReportSpam(ctx context.Context, sessionID string, report SpamReport) (SpamReportResult, error) {
	res, err := b.client.ReportSpam(ctx, sessionID, waxum.SpamReport{
		MessageID:        report.MessageID,
		MessageTimestamp: report.MessageTimestamp,
		FromJID:          report.FromJID,
		ParticipantJID:   report.ParticipantJID,
		GroupJID:         report.GroupJID,
		GroupSubject:     report.GroupSubject,
		SpamFlow:         report.SpamFlow,
		MediaType:        report.MediaType,
	})
	if err != nil {
		return SpamReportResult{}, mapErr(err)
	}
	return SpamReportResult{Success: res.Success, ReportID: res.ReportID}, nil
}

// TCTokenIssue satisfies SessionOps.
func (b *waxumBackend) TCTokenIssue(ctx context.Context, sessionID string, jids []string) ([]TCToken, error) {
	tokens, err := b.client.TCTokenIssue(ctx, sessionID, jids)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]TCToken, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, TCToken{JID: t.JID, Timestamp: t.Timestamp})
	}
	return out, nil
}

// TCTokenGet satisfies SessionOps.
func (b *waxumBackend) TCTokenGet(ctx context.Context, sessionID, jid string) (TCTokenInfo, error) {
	info, err := b.client.TCTokenGet(ctx, sessionID, jid)
	if err != nil {
		return TCTokenInfo{}, mapErr(err)
	}
	return TCTokenInfo{
		JID:             info.JID,
		TokenTimestamp:  info.TokenTimestamp,
		SenderTimestamp: info.SenderTimestamp,
		Found:           info.Found,
	}, nil
}

// TCTokenList satisfies SessionOps.
func (b *waxumBackend) TCTokenList(ctx context.Context, sessionID string) ([]string, error) {
	jids, err := b.client.TCTokenList(ctx, sessionID)
	if err != nil {
		return nil, mapErr(err)
	}
	return jids, nil
}

// TCTokenPrune satisfies SessionOps.
func (b *waxumBackend) TCTokenPrune(ctx context.Context, sessionID string) (int, error) {
	n, err := b.client.TCTokenPrune(ctx, sessionID)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// var _ guards below ensure a typo'd signature fails to compile instead of
// silently falling back to a bogus ErrUnsupported at runtime.
var (
	_ GatewayHealth = (*waxumBackend)(nil)
	_ SessionOps    = (*waxumBackend)(nil)
)
