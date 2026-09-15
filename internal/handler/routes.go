// Package handler wires HTTP routes to thin page and API handlers.
package handler

import (
	"io/fs"
	"net/http"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// Deps carries everything the handlers need.
type Deps struct {
	Renderer   *renderer.Renderer
	Sessions   *auth.Store
	Middleware *auth.Middleware
	Manager    *wa.Manager
	Messages   message.Repository
	// MessageStats is the read side of the audit log (the /logs page and the real
	// dashboard). It is the same concrete repo as Messages, held by its analytics
	// type so the write interface stays free of query methods.
	MessageStats *message.SQLRepository
	APIClients   *apiclient.Repository
	// Calls is the wa_calls persistence seam (Phase 6's placed-call
	// handlers write through it; nothing calls it yet in this phase).
	Calls call.Repository
	// Inbound is the read side of wa_inbound_messages for GET
	// /api/v1/inbound (GetAPIInbound, api_inbound.go). nil is legal — every
	// test server that predates this endpoint leaves it unset — and means
	// the endpoint answers 503 "inbound log unavailable" rather than
	// panicking, the same convention Calls/Objects use.
	Inbound inbound.Repository
	// Objects is the media-archival seam onto MinIO (Phase 6's media-send
	// handler uploads through it before enqueuing; nothing calls it yet in
	// this phase). An interface, not *minio.Client, so handler tests can
	// substitute a fake — see storage.ObjectStore's doc comment.
	Objects storage.ObjectStore

	// Media/Calls config (Phase 6a — internal/config/media.go's loadMedia).
	// Copied onto Deps like every other resolved config value here (BasePath,
	// CookieSecure, SessionTTL, …) rather than threading *config.Config
	// itself, which this package has never depended on.
	MediaMaxBytes          int64
	MediaUploadTimeout     time.Duration
	MediaUploadConcurrency int
	MediaArchive           bool
	// MediaMaxBytes{Image,Video,Audio,Document} are the per-kind caps
	// parseMediaSendRequest (api_media.go) checks a decoded upload against,
	// on top of the socket-level MediaMaxBytes above. A zero value here
	// (any handler test built before this field existed) falls back to
	// MediaMaxBytes for that kind — see mediaKindLimit's doc comment.
	MediaMaxBytesImage    int64
	MediaMaxBytesVideo    int64
	MediaMaxBytesAudio    int64
	MediaMaxBytesDocument int64
	// CallsEnabled gates every /calls/* endpoint — see api_calls.go's
	// callsDisabledMessage.
	CallsEnabled bool

	// SendRate/SendBurst are the configured per-session token-bucket floor
	// (internal/wa/sender_limits.go, WSM_SEND_RATE/WSM_SEND_BURST) — copied
	// onto Deps like every other resolved config value here so
	// /outgoing-chat (outgoing_chat.go) can render the expected sustained
	// gap (1/SendRate) and flag a session whose observed average gap is
	// running below it. Zero (any handler test built before this field
	// existed) falls back to the same 1.0/3 defaults config.Load itself uses
	// — see effectiveSendRate/effectiveSendBurst in outgoing_chat.go. This
	// package deliberately does not import internal/config (see the
	// MediaMaxBytes comment above), so the fallback values are a plain
	// local copy, not a shared constant.
	SendRate  float64
	SendBurst int

	// Convo is the Self Convo persistence seam (internal/domain/convo) —
	// template CRUD and run listing/cancel go through it directly from the
	// handlers, same as Calls above. nil is legal (older test servers);
	// guarded the same way h.Calls is (recentCalls, calls_ui.go).
	Convo *convo.Repository
	// SelfConvo is the running scheduler (internal/selfconvo), wired by
	// cmd/app/main.go via startSelfConvo only when SelfConvoEnabled is true;
	// nil otherwise. Trigger/Cancel go through it; template management does
	// not need it at all (see self_convo_ui.go's doc comment on why
	// template CRUD is not gated by SelfConvoEnabled).
	SelfConvo *selfconvo.Runner
	// SelfConvoEnabled gates every /self-convo/runs/* endpoint — see
	// self_convo_ui.go's selfConvoDisabledMessage.
	SelfConvoEnabled bool
	// Planner is the Convo scheduling background loop (internal/selfconvo),
	// wired by cmd/app/main.go's startSelfConvo alongside SelfConvo — nil
	// under the exact same condition (SelfConvoEnabled off). Every schedule
	// pause/resume/stop action goes through it (self_convo_schedule_ui.go)
	// so those refusals live in one place; plain schedule CRUD and listing
	// go through Convo directly and need no Planner at all.
	Planner *selfconvo.Planner

	SessionTTL time.Duration
	DarkMode   bool
	// PlatformURL is the absolute backoffice url for the sidebar's back-link.
	PlatformURL string
	// HostLabel is operator-supplied text ("MINIPC"/"LAPTOP") rendered as a
	// header chip so an operator can tell which machine served the page
	// during a failover — see config.Config.HostLabel's doc comment. ""
	// hides the chip.
	HostLabel string
	AssetsFS  fs.FS // embedded FS rooted at the app dir (web/..., openapi.yaml)

	// BasePath is the mount prefix for the BROWSER surface ("/wsm" when served
	// under a path, "" at a host root). Derived from WA_BASE_URL by config.Load,
	// so what we serve and what we advertise cannot drift apart.
	BasePath string

	// Admin is the single local operator credential guarding the browser
	// surface, resolved from the environment at boot (config.AdminUser /
	// AdminPasswordHash). The zero value authenticates nobody.
	Admin auth.AdminCredential
	// LoginThrottle rate-limits failed logins per IP; nil means no throttling.
	LoginThrottle *auth.Throttle
	CookieSecure  bool
}

// New builds the full route table on a Go 1.22+ ServeMux (docs/08).
//
// ── THE SPLIT IS THE PUBLIC/INTERNAL BOUNDARY, NOT A FILE-SIZE TRICK ────────
//
// mountUI (routes_ui.go) serves the operator's browser surface and is mounted
// under d.BasePath. mountAPI (routes_api.go) serves the JSON API and stays at
// the ROOT, unprefixed, and is not publicly routed at all.
//
// That asymmetry is deliberate. Machine callers dial WSM_URL + "/api/v1/..."
// over container DNS on a private network and never reach this service through
// an ingress, so prefixing the API would break every one of them to gain
// nothing. Leaving it at the root also keeps /api/v1 from clashing with a parent
// app's own /api when the UI is mounted under a path prefix.
//
// NOTHING IS STRIPPED: the service owns its prefix end to end. An ingress that
// strips the prefix would pass every curl check against the API and STILL break
// the login POST, because the login page renders its own form action from
// BasePath — the failure looks like "the form does nothing" and points nowhere
// near the proxy.
func New(d Deps) http.Handler {
	h := &handlers{Deps: d, mediaSem: make(chan struct{}, mediaUploadConcurrency(d.MediaUploadConcurrency))}
	mux := http.NewServeMux()
	h.mountUI(mux)
	h.mountAPI(mux)
	return withLogging(mux)
}

// mediaUploadConcurrency clamps Deps.MediaUploadConcurrency to at least 1:
// a zero-value Deps (every handler test that predates Phase 6a) must not
// hand make() a zero-sized channel, which would deadlock the FIRST media
// upload it ever received instead of the intended "no concurrent uploads
// configured" no-op.
func mediaUploadConcurrency(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// handlers groups the route methods around the shared dependencies.
type handlers struct {
	Deps
	// mediaSem bounds concurrent media uploads at cfg.MediaUploadConcurrency
	// (see api_media.go) — sendQueueCap (internal/wa) bounds the send
	// QUEUE, not uploads, which happen before enqueue.
	mediaSem chan struct{}
}

// cookiePath is the Path every cookie this service sets must carry: the mount,
// or "/" when unmounted. Centralised because Set and Clear must agree exactly —
// a browser keys a cookie by (name, domain, path), so a logout written at a
// different Path leaves the original in place and the operator stays signed in
// with nothing in any log to say why.
func (h *handlers) cookiePath() string {
	if h.BasePath == "" {
		return "/"
	}
	return h.BasePath
}

// cookieOpts is what internal/auth needs to write the session cookie: where it
// applies, and whether it may cross a plaintext hop.
func (h *handlers) cookieOpts() auth.CookieOpts {
	return auth.CookieOpts{Path: h.cookiePath(), Secure: h.CookieSecure}
}
