package wa

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by a backend for a capability it does not (yet)
// implement. Nothing in this interface needs it today — both whatsmeow and
// the planned waxum backend implement every method below. It exists now,
// reserved, for the capabilities that motivated this migration in the first
// place (media attachments, calls): those are NOT part of Backend yet, on
// purpose — adding a method only one backend can honour, in a phase with no
// second backend to prove the split against, is how an interface ends up
// wrong. They get added, with ErrUnsupported wired to whichever backend
// lacks them, when they actually land.
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
