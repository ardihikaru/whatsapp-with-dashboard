package wa

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by a backend for a capability it does not
// implement.
//
// Media (SendMedia) and calls (CallTTS/CallPlay/CallTerminate) — the two
// capabilities that motivated the whatsmeow-to-waxum migration in the first
// place — are now real Backend methods below, exactly as this comment
// originally promised: meowBackend answers all four with ErrUnsupported
// (backend_meow_unsupported.go), waxumBackend implements them for real
// (backend_waxum_media.go, backend_waxum_calls.go).
//
// Gateway ADMINISTRATION — pause/resume, auto-reconnect, history-sync,
// appstate resync, spam reporting, trust-conversation tokens (12 operations,
// see SessionOps), the media store's upload/download half (MediaStore),
// webhook management (WebhookAdmin) and a fleet health probe
// (GatewayHealth) — deliberately did NOT follow media/calls onto Backend
// itself. That would have meant ~25 more Backend methods, every one of them
// a stub on meowBackend (whatsmeow has no REST gateway to administer this
// way at all), and a compile break in both of this package's test fakes
// (backend_test.go's fakeBackend, internal/handler/pairing_test.go's
// fakeStartPairingBackend) for each one added — exactly the "adding a
// method only one backend can honour... is how an interface ends up wrong"
// failure this comment used to warn about, just for a much larger batch of
// methods than the original two.
//
// So gateway administration instead lives behind OPTIONAL capability
// interfaces (capabilities.go: MediaStore, SessionOps, WebhookAdmin,
// GatewayHealth), reached through a type assertion on Backend — the way
// http.Flusher works, not the way Backend's core methods work. waxumBackend
// implements every one of them; meowBackend implements none (see
// TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface — a stray stub
// there would silently enable a gateway-admin path that has nothing behind
// it). Manager exposes them via manager_capabilities.go's accessors
// (GatewayHealth/MediaStore/SessionOps/WebhookAdmin), which do the type
// assertion once and return ErrUnsupported on failure — that assertion IS
// "wired to whichever backend lacks it" for this half of the surface,
// exactly like the four Backend methods below are for media/calls.
//
// Why media/calls got the heavier (Backend-method) treatment while
// everything else got the lighter (capability-interface) one: media and
// calls sit on trySend's hot path (sender.go) and ManagedSession's own
// call-placing wrappers (calls.go) — code that already has to reach through
// Backend for SendText and benefits from the same static, no-assertion
// shape. Gateway administration is reached only from new, dedicated HTTP
// handlers with no such existing call site to match, and there are simply
// too many of them to put on every backend unconditionally.
var ErrUnsupported = errors.New("wa: capability not supported by this backend")

// Backend is the transport this package drives ManagedSession through. Two
// implementations are planned: backend_meow.go (whatsmeow, in-process,
// today's only wiring — see TestWhatsmeowBackendIsTheOnlyBackendWiredToday)
// and a future waxum backend (a Rust REST gateway, phase 2) behind a config
// flag. Manager holds exactly one Backend; every ManagedSession it creates
// shares it.
//
// Every method here was chosen by moving an existing whatsmeow call site
// behind an interface, not by designing ahead of need — see the file move
// list in backend_meow.go's header comment for where each one came from.
//
// A design note two levels of code need, so it is written once, here:
//
// ManagedSession keeps `client *whatsmeow.Client` and `jid types.JID` as
// concrete whatsmeow types (session.go) rather than behind an opaque
// `backendState any` slot. That was a real choice, not an oversight:
//   - sender.go's Enqueue and trySend read s.state under s.mu as a cheap,
//     synchronous "is there a live transport" check, on the hot path (every
//     enqueued message) — NOT s.client, which is whatsmeow-only and stays
//     nil forever under the waxum backend (backend_waxum.go). Enqueue is
//     explicitly transport-agnostic and must NOT move behind Backend — an
//     `any` slot would force it to either grow a type switch (defeating the
//     point of an interface) or route a same-package field read through a
//     virtual call for no reason.
//   - jid carries whatsmeow's Device/RawAgent/Integrator, which round-trip
//     through wa_sessions.jid and matter to GetDevice on reconnect (see
//     jid.go's doc comment). Narrowing it to this package's JID{User,Server}
//     would silently pick the wrong device row for any linked companion
//     device with a nonzero Device — a real behaviour change.
//
// Because backend_meow.go lives in this same package, it can still read
// those fields directly under s.mu wherever it needs whatsmeow-specific
// behaviour (attachClientLocked, handleEvent, pairPump, …) — an opaque slot
// would not have bought isolation from THIS backend, only made the shared
// (transport-agnostic) code above worse. A second backend (waxum) is
// expected to need real backend-owned state (an HTTP client, a poll-loop
// cancel func, …); it gets that via its own unexported struct fields on its
// Backend implementation, not via ManagedSession.
type Backend interface {
	// Name identifies the backend for logging/diagnostics ("whatsmeow").
	Name() string

	// StartPairing begins linking a new device (QR or phone-number code) and
	// drives it to a terminal outcome in the background. Mirrors the
	// pre-seam ManagedSession.StartPairing exactly; see backend_meow.go.
	StartPairing(ctx context.Context, s *ManagedSession, mode PairMode) error

	// Connect (re)establishes the transport for an already-paired session —
	// the client-attach-and-dial work that used to be inline in Activate.
	// Returns ErrDeviceGone if the backend has no credentials for s.jid.
	Connect(ctx context.Context, s *ManagedSession) error

	// Disconnect drops the transport but keeps credentials — the backend
	// half of what used to be inline in Deactivate.
	Disconnect(ctx context.Context, s *ManagedSession) error

	// Logout revokes the paired device's credentials (or, if it was never
	// paired, just discards the local transport) — the backend half of what
	// used to be inline in Manager.Delete.
	Logout(ctx context.Context, s *ManagedSession) error

	// SendText sends one plain-text message and returns the backend's
	// message id. Called with a context already carrying trySend's 30s
	// deadline.
	SendText(ctx context.Context, s *ManagedSession, to JID, text string) (waMessageID string, err error)

	// JoinedGroups lists the groups the paired device currently belongs to.
	// Implemented on EVERY backend, whatsmeow included — this already works
	// today (the pre-seam ManagedSession.JoinedGroups) and is reachable at
	// GET /api/v1/sessions/{id}/groups; stubbing it on whatsmeow would be a
	// regression on the one path that exists to be a rollback target.
	JoinedGroups(ctx context.Context, s *ManagedSession) ([]GroupInfo, error)

	// SendMedia sends one media message (image/video/audio/document) built
	// from a descriptor already uploaded to the backend's media store (see
	// MediaStore.UploadMedia) and returns the backend's message id. Mirrors
	// SendText's shape exactly — see ErrUnsupported's doc comment for why
	// this is a core Backend method rather than a capability interface.
	// meowBackend answers ErrUnsupported (backend_meow_unsupported.go).
	SendMedia(ctx context.Context, s *ManagedSession, to JID, m OutboundMedia) (waMessageID string, err error)

	// CallTTS places a voice call to `to` and speaks `text` via
	// text-to-speech (`voice` selects the TTS voice; backend-defined
	// default when empty — see waxum.DefaultTTSVoice for waxumBackend's).
	// meowBackend answers ErrUnsupported.
	CallTTS(ctx context.Context, s *ManagedSession, to JID, text, voice string) (CallOutcome, error)

	// CallPlay places a voice call to `to` and plays a pre-recorded audio
	// file fetched from audioURL. meowBackend answers ErrUnsupported.
	CallPlay(ctx context.Context, s *ManagedSession, to JID, audioURL string) (CallOutcome, error)

	// CallTerminate ends an in-progress call identified by callID, placed
	// to/from peer. reason is backend-defined and optional (empty lets the
	// backend apply its own default — see waxumBackend's CallTerminate).
	// meowBackend answers ErrUnsupported.
	CallTerminate(ctx context.Context, s *ManagedSession, peer JID, callID, reason string) error

	// Live reports whether the backend currently believes it has a live
	// connection for s. Snapshot (session.go) calls this while HOLDING s.mu,
	// on every 2s HTMX row poll and on every send's load-balancing pick
	// (load_balance.go) — so an implementation MUST be O(1) and MUST NOT do
	// I/O. whatsmeow already tracks this in-process and just forwards
	// Client.IsConnected(); a REST backend is expected to back this with an
	// atomic.Bool kept current by its own liveness loop, never by an HTTP
	// call made from inside this method.
	Live(s *ManagedSession) bool

	// Close releases backend-level resources (e.g. the whatsmeow sqlite
	// container) during Manager.Close, after every session's own teardown.
	Close()
}
