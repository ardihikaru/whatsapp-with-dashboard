package wa

import "context"

// This file holds the optional capability interfaces gateway ADMINISTRATION
// is exposed through — see backend.go's ErrUnsupported doc comment for why
// this half of the waxum surface is a type-asserted optional interface
// rather than a Backend method the way media/calls are. waxumBackend
// implements every interface here (backend_waxum_ops.go,
// backend_waxum_webhooks.go, backend_waxum_media.go's MediaStore half);
// meowBackend implements none of them — see
// TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface. Reached via
// manager_capabilities.go's Manager accessors, never asserted against
// Backend directly outside this package.

// GatewayHealth probes whether the backend's own gateway process (not any
// particular session) is reachable and healthy. whatsmeow has no separate
// gateway process to probe, so meowBackend does not implement this at all.
type GatewayHealth interface {
	Health(ctx context.Context) error

	// Reachable reports the LAST cached verdict on gateway reachability —
	// O(1), no I/O, maintained by the SSE event pump
	// (backend_waxum_events.go, Phase 8). ok is false until that pump has
	// recorded a first verdict (a cold boot, or WSM_WAXUM_EVENTS=false —
	// see that file's doc comment), in which case reachable is
	// meaningless and callers must treat health as unknown, not "down".
	// Exists specifically for the unauthenticated GET /api/v1/health
	// container healthcheck (api.go's GetHealth), which must NEVER call
	// the backend inline: CLAUDE.md's waxum rate-limit hazard note — an
	// inline call on every liveness probe would add a network hop to a
	// route checked on every liveness interval AND feed waxum's own
	// tower_governor limiter for no reason a liveness check needs.
	Reachable() (reachable, ok bool)
}

// MediaStore is the upload/download half of media handling — the part NOT
// on Backend itself (see OutboundMedia's doc comment for why SendMedia,
// unlike this, is a core Backend method). sessionID is a plain string, not
// *ManagedSession, deliberately: MediaStore.UploadMedia is called from the
// HTTP handler BEFORE a message is enqueued (see the plan's "workstream D",
// upload does not ride the send queue) and has no ManagedSession in hand at
// that point, only the id off the URL path.
type MediaStore interface {
	UploadMedia(ctx context.Context, sessionID string, kind MediaKind, filename, mimetype string, data []byte) (MediaDescriptor, error)
	DownloadMedia(ctx context.Context, sessionID string, d MediaDescriptor, kind MediaKind) ([]byte, error)
}

// AutoReconnectState is AutoReconnect/SetAutoReconnect's result, mirroring
// waxum.AutoReconnect field-for-field.
type AutoReconnectState struct {
	Enabled    bool
	ErrorCount uint32
}

// AppStateResyncResult mirrors waxum.AppStateResync field-for-field.
type AppStateResyncResult struct {
	Synced    []string
	Fatal     []string
	Retryable []string
	Skipped   []string
	AllSynced bool
}

// SpamReport is the operator-supplied half of a spam report. MessageID and
// MessageTimestamp are required — see waxum.SpamReport's doc comment for
// why omitting MessageTimestamp 422s against real waxum; that trap is
// preserved here rather than re-litigated, since SessionOps.ReportSpam's
// waxum implementation passes this straight through field-for-field.
type SpamReport struct {
	MessageID        string
	MessageTimestamp int64
	FromJID          string
	ParticipantJID   string
	GroupJID         string
	GroupSubject     string
	SpamFlow         string
	MediaType        string
}

// SpamReportResult mirrors waxum.SpamReportResponse.
type SpamReportResult struct {
	Success  bool
	ReportID *string
}

// TCToken is one issued trust-conversation token, mirroring waxum.TCToken.
type TCToken struct {
	JID       string
	Timestamp int64
}

// TCTokenInfo mirrors waxum.TCTokenInfo — see that type's doc comment for
// why SenderTimestamp is a pointer and TokenTimestamp is not.
type TCTokenInfo struct {
	JID             string
	TokenTimestamp  int64
	SenderTimestamp *int64
	Found           bool
}

// SessionOps is the 13 waxum session-administration operations that have no
// analogue on whatsmeow at all: pause/resume (and a read-only paused
// query), auto-reconnect and history-sync toggles, appstate resync, spam
// reporting, and the trust-conversation-token family. Named to match
// waxum's own grouping (src/handlers/operations.rs), not Backend's naming,
// since none of these are Backend methods.
type SessionOps interface {
	// IsPaused reports whether the session is currently paused WITHOUT
	// mutating anything — unlike Pause/Resume, which both report the
	// paused state only as a side effect of changing it. waxum has no
	// dedicated pause-status endpoint; "paused" is one field of the same
	// GET .../status payload the connection-state poller already reads
	// (waxum.Status.Paused, backend_waxum.go's statusPump), so this is a
	// read of that same endpoint, not a new one. Added specifically for
	// the operations-snapshot handler (Phase 6b's GET .../operations),
	// which must report current state without ever calling Pause or
	// Resume itself.
	IsPaused(ctx context.Context, sessionID string) (bool, error)
	Pause(ctx context.Context, sessionID string) (paused bool, err error)
	Resume(ctx context.Context, sessionID string) (paused bool, err error)
	AutoReconnect(ctx context.Context, sessionID string) (AutoReconnectState, error)
	SetAutoReconnect(ctx context.Context, sessionID string, enabled bool) (AutoReconnectState, error)
	HistorySync(ctx context.Context, sessionID string) (skip bool, err error)
	SetHistorySync(ctx context.Context, sessionID string, skip bool) (bool, error)
	AppStateResync(ctx context.Context, sessionID string, collections []string, mode string) (AppStateResyncResult, error)
	ReportSpam(ctx context.Context, sessionID string, report SpamReport) (SpamReportResult, error)
	TCTokenIssue(ctx context.Context, sessionID string, jids []string) ([]TCToken, error)
	TCTokenGet(ctx context.Context, sessionID, jid string) (TCTokenInfo, error)
	TCTokenList(ctx context.Context, sessionID string) ([]string, error)
	TCTokenPrune(ctx context.Context, sessionID string) (prunedCount int, err error)
}

// WebhookSpec is what RegisterWebhook takes: the operator-supplied half of
// a subscription.
type WebhookSpec struct {
	URL    string
	Secret string
	Events []string
}

// WebhookInfo is a registered webhook, mirroring waxum.WebhookConfigWithID.
// Secret is a pointer for the same reason as there — an HMAC signing key
// that must never be logged or rendered back to an operator verbatim (see
// that type's doc comment).
type WebhookInfo struct {
	ID      string
	URL     string
	Events  []string
	Secret  *string
	Enabled bool
}

// DLQEntry is one dead-lettered webhook delivery, mirroring
// waxum.WebhookDLQEntry field-for-field.
type DLQEntry struct {
	ID         string
	SessionID  string
	WebhookURL string
	Event      string
	Payload    string
	LastError  string
	Attempts   int
	FailedAt   int64
}

// ContactCheck is one entry of ContactDirectory.CheckContacts' result,
// mirroring waxum.ContactCheck field-for-field — see that type's doc
// comment for why JID is a plain string (not a pointer) despite waxum's
// own field being Option<String>, and why Phone is not guaranteed to echo
// the input verbatim.
type ContactCheck struct {
	Phone        string
	JID          string
	IsRegistered bool
}

// ContactDirectory is whether a phone number is a registered WhatsApp
// account — whatsmeow has no analogue at all (it can only discover this as
// a side effect of actually messaging a number), so meowBackend implements
// none of it, the same as every other interface in this file. sessionID is
// a plain string, not *ManagedSession, for the same reason as MediaStore's
// doc comment: reached from a dedicated HTTP handler keyed off the URL
// path id, with no ManagedSession in hand.
type ContactDirectory interface {
	CheckContacts(ctx context.Context, sessionID string, phones []string) ([]ContactCheck, error)
}

// WebhookAdmin is the 6 webhook-management operations scoped to one session
// plus the one fleet-level operation (ReenableAllWebhooks, which is NOT
// scoped to a session — mirroring waxum.ReenableAllWebhooks's own doc
// comment on why it takes no session id).
type WebhookAdmin interface {
	ListWebhooks(ctx context.Context, sessionID string) ([]WebhookInfo, error)
	RegisterWebhook(ctx context.Context, sessionID string, spec WebhookSpec) (WebhookInfo, error)
	UnregisterWebhook(ctx context.Context, sessionID, webhookID string) error
	EnableWebhook(ctx context.Context, sessionID, webhookID string) error
	WebhookDLQ(ctx context.Context, sessionID string) ([]DLQEntry, error)
	ReplayWebhookDLQEntry(ctx context.Context, sessionID, entryID string) error
	ReenableAllWebhooks(ctx context.Context) error
}
