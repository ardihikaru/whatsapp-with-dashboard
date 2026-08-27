package handler

import (
	"context"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// fakeOpsBackend layers wa.GatewayHealth, wa.SessionOps and wa.WebhookAdmin
// onto fakeGatewayBackend (media_calls_test_backend_test.go), giving Phase
// 6b's handler tests a backend that is shaped like waxum — supporting every
// capability interface, the way a real waxumBackend does — rather than a
// second, unrelated fake.
//
// Every capability method here is hook-driven, same pattern as
// fakeGatewayBackend's ttsFn/playFn/termFn, but with the OPPOSITE default:
// fakeGatewayBackend's hooks default to wa.ErrUnsupported because a
// whatsmeow-shaped backend genuinely does not support calls/media. This
// fake exists specifically to BE the waxum-shaped backend, so an unset hook
// here answers a zero-value success instead — a test that never sets
// pauseFn is exercising "waxum answered with nothing interesting to
// report," not "unsupported."
type fakeOpsBackend struct {
	*fakeGatewayBackend

	healthFn func() error
	// reachableFn, when set, overrides Reachable's return — see
	// wa.GatewayHealth.Reachable's doc comment. Unset answers (false,
	// false): "no cached verdict yet", the honest default for a fake that
	// never runs an event pump.
	reachableFn func() (reachable, ok bool)

	isPausedFn         func() (bool, error)
	pauseFn            func() (bool, error)
	resumeFn           func() (bool, error)
	autoReconnectFn    func() (wa.AutoReconnectState, error)
	setAutoReconnectFn func(enabled bool) (wa.AutoReconnectState, error)
	historySyncFn      func() (bool, error)
	setHistorySyncFn   func(skip bool) (bool, error)
	appStateResyncFn   func(collections []string, mode string) (wa.AppStateResyncResult, error)
	reportSpamFn       func(report wa.SpamReport) (wa.SpamReportResult, error)
	tcTokenIssueFn     func(jids []string) ([]wa.TCToken, error)
	tcTokenGetFn       func(jid string) (wa.TCTokenInfo, error)
	tcTokenListFn      func() ([]string, error)
	tcTokenPruneFn     func() (int, error)

	listWebhooksFn          func() ([]wa.WebhookInfo, error)
	registerWebhookFn       func(spec wa.WebhookSpec) (wa.WebhookInfo, error)
	unregisterWebhookFn     func(webhookID string) error
	enableWebhookFn         func(webhookID string) error
	webhookDLQFn            func() ([]wa.DLQEntry, error)
	replayWebhookDLQEntryFn func(entryID string) error
	reenableAllWebhooksFn   func() error
}

func (f *fakeOpsBackend) Health(context.Context) error {
	if f.healthFn != nil {
		return f.healthFn()
	}
	return nil
}

func (f *fakeOpsBackend) Reachable() (bool, bool) {
	if f.reachableFn != nil {
		return f.reachableFn()
	}
	return false, false
}

func (f *fakeOpsBackend) IsPaused(context.Context, string) (bool, error) {
	if f.isPausedFn != nil {
		return f.isPausedFn()
	}
	return false, nil
}

func (f *fakeOpsBackend) Pause(context.Context, string) (bool, error) {
	if f.pauseFn != nil {
		return f.pauseFn()
	}
	return true, nil
}

func (f *fakeOpsBackend) Resume(context.Context, string) (bool, error) {
	if f.resumeFn != nil {
		return f.resumeFn()
	}
	return false, nil
}

func (f *fakeOpsBackend) AutoReconnect(context.Context, string) (wa.AutoReconnectState, error) {
	if f.autoReconnectFn != nil {
		return f.autoReconnectFn()
	}
	return wa.AutoReconnectState{}, nil
}

func (f *fakeOpsBackend) SetAutoReconnect(_ context.Context, _ string, enabled bool) (wa.AutoReconnectState, error) {
	if f.setAutoReconnectFn != nil {
		return f.setAutoReconnectFn(enabled)
	}
	return wa.AutoReconnectState{Enabled: enabled}, nil
}

func (f *fakeOpsBackend) HistorySync(context.Context, string) (bool, error) {
	if f.historySyncFn != nil {
		return f.historySyncFn()
	}
	return false, nil
}

func (f *fakeOpsBackend) SetHistorySync(_ context.Context, _ string, skip bool) (bool, error) {
	if f.setHistorySyncFn != nil {
		return f.setHistorySyncFn(skip)
	}
	return skip, nil
}

func (f *fakeOpsBackend) AppStateResync(_ context.Context, _ string, collections []string, mode string) (wa.AppStateResyncResult, error) {
	if f.appStateResyncFn != nil {
		return f.appStateResyncFn(collections, mode)
	}
	return wa.AppStateResyncResult{AllSynced: true}, nil
}

func (f *fakeOpsBackend) ReportSpam(_ context.Context, _ string, report wa.SpamReport) (wa.SpamReportResult, error) {
	if f.reportSpamFn != nil {
		return f.reportSpamFn(report)
	}
	return wa.SpamReportResult{Success: true}, nil
}

func (f *fakeOpsBackend) TCTokenIssue(_ context.Context, _ string, jids []string) ([]wa.TCToken, error) {
	if f.tcTokenIssueFn != nil {
		return f.tcTokenIssueFn(jids)
	}
	return nil, nil
}

func (f *fakeOpsBackend) TCTokenGet(_ context.Context, _, jid string) (wa.TCTokenInfo, error) {
	if f.tcTokenGetFn != nil {
		return f.tcTokenGetFn(jid)
	}
	return wa.TCTokenInfo{JID: jid}, nil
}

func (f *fakeOpsBackend) TCTokenList(context.Context, string) ([]string, error) {
	if f.tcTokenListFn != nil {
		return f.tcTokenListFn()
	}
	return nil, nil
}

func (f *fakeOpsBackend) TCTokenPrune(context.Context, string) (int, error) {
	if f.tcTokenPruneFn != nil {
		return f.tcTokenPruneFn()
	}
	return 0, nil
}

func (f *fakeOpsBackend) ListWebhooks(context.Context, string) ([]wa.WebhookInfo, error) {
	if f.listWebhooksFn != nil {
		return f.listWebhooksFn()
	}
	return nil, nil
}

func (f *fakeOpsBackend) RegisterWebhook(_ context.Context, _ string, spec wa.WebhookSpec) (wa.WebhookInfo, error) {
	if f.registerWebhookFn != nil {
		return f.registerWebhookFn(spec)
	}
	return wa.WebhookInfo{URL: spec.URL, Events: spec.Events}, nil
}

func (f *fakeOpsBackend) UnregisterWebhook(_ context.Context, _, webhookID string) error {
	if f.unregisterWebhookFn != nil {
		return f.unregisterWebhookFn(webhookID)
	}
	return nil
}

func (f *fakeOpsBackend) EnableWebhook(_ context.Context, _, webhookID string) error {
	if f.enableWebhookFn != nil {
		return f.enableWebhookFn(webhookID)
	}
	return nil
}

func (f *fakeOpsBackend) WebhookDLQ(context.Context, string) ([]wa.DLQEntry, error) {
	if f.webhookDLQFn != nil {
		return f.webhookDLQFn()
	}
	return nil, nil
}

func (f *fakeOpsBackend) ReplayWebhookDLQEntry(_ context.Context, _, entryID string) error {
	if f.replayWebhookDLQEntryFn != nil {
		return f.replayWebhookDLQEntryFn(entryID)
	}
	return nil
}

func (f *fakeOpsBackend) ReenableAllWebhooks(context.Context) error {
	if f.reenableAllWebhooksFn != nil {
		return f.reenableAllWebhooksFn()
	}
	return nil
}

var (
	_ wa.GatewayHealth = (*fakeOpsBackend)(nil)
	_ wa.SessionOps    = (*fakeOpsBackend)(nil)
	_ wa.WebhookAdmin  = (*fakeOpsBackend)(nil)
)
