package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

const maxBodyBytes = 64 * 1024

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	LoginID  string `json:"login_id"`
	Secret   string `json:"secret"`
}

type loginResponse struct {
	Token     string  `json:"token"`
	ExpiresAt *string `json:"expires_at"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// backendBusyRetryAfterSeconds is the Retry-After hint sent alongside
// wa.ErrBackendBusy (see writeBackendBusy). It is a fixed, conservative
// value rather than something derived from waxum's own Retry-After header:
// by the time a handler sees ErrBackendBusy, internal/waxum.Client.do has
// already retried the underlying 429 with backoff (honouring any
// Retry-After waxum sent) and given up — this is what a CALLER of WSM's own
// API should wait before trying again, a separate question from what WSM
// itself waited internally.
const backendBusyRetryAfterSeconds = "5"

// writeBackendBusy answers wa.ErrBackendBusy: 503, not 429. 429 already
// means something else in this API (wa.ErrQueueFull — THIS session's own
// send queue is full, see errors.go); reusing it here would tell an API
// consumer it is the one being throttled, when the actual cause is the
// WhatsApp backend (waxum) rate-limiting WSM itself. 503 + Retry-After is
// the more honest signal: temporarily unavailable, retry shortly, not "you
// are sending too fast".
func writeBackendBusy(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", backendBusyRetryAfterSeconds)
	writeError(w, http.StatusServiceUnavailable, message)
}

// writeWAError maps a wa.Err* sentinel to its HTTP status and writes it.
// internal/wa/errors.go's header comment documents this exact map — keep
// the two in step whenever either changes.
//
// Extracted from apiToggle (previously the only hand-rolled copy of this
// switch, covering ErrNotPaired/ErrDeviceGone/ErrBackendBusy) as the FIRST
// thing Phase 6a does, precisely because that phase adds 20+ endpoints that
// all need the same mapping: a switch statement copy-pasted 20 times is how
// this status map rots the moment one sentinel's meaning changes and only
// some copies get updated. Every handler this package adds from here on
// funnels its wa.* errors through this one function instead.
func writeWAError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, wa.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, wa.ErrNotPaired),
		errors.Is(err, wa.ErrDeviceGone),
		errors.Is(err, wa.ErrWrongState),
		errors.Is(err, wa.ErrNotConnected):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, wa.ErrNotAGroupJID):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, wa.ErrQueueFull):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, wa.ErrRejectedByBackend):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, wa.ErrAlreadyExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, wa.ErrBackendBusy):
		writeBackendBusy(w, err.Error())
	case errors.Is(err, wa.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// noRouteMessage is the plain 409 body every automatic-routing endpoint
// answers when SelectConnected finds nothing. Kept as its own constant so
// noAutoRouteMessage's "unchanged normally" guarantee below is checkable
// against the literal string, not just its shape.
const noRouteMessage = "no connected session available"

// masterExclusionHint is appended to noRouteMessage ONLY when
// noAutoRouteMessage determines a connected master session exists and was
// excluded — see that function's doc comment.
const masterExclusionHint = " (the only connected session(s) are master sessions; set allow_master to route through one)"

// selectForSend picks the session for an automatic-routing send. It is the one
// place the two routing MODES are chosen between, so every endpoint that offers
// master_only resolves it identically instead of each re-deriving the
// precedence. masterOnly wins over allowMaster — see groupSendRequest.MasterOnly.
func selectForSend(r *http.Request, m *wa.Manager, masterOnly, allowMaster bool) (*wa.ManagedSession, wa.Snapshot, bool) {
	if masterOnly {
		return m.SelectMaster(r.Context())
	}
	return m.SelectConnected(r.Context(), allowMaster)
}

// noSendRouteMessage builds the 409 body for whichever mode selectForSend used.
// Pure, like noAutoRouteMessage, so both branches stay unit-testable without a
// live session.
func noSendRouteMessage(snaps []wa.Snapshot, masterOnly, allowMaster bool) string {
	if masterOnly {
		return noMasterRouteMessage
	}
	return noAutoRouteMessage(snaps, allowMaster)
}

// noMasterRouteMessage is the 409 body for a master_only send when no
// connected master exists. It is deliberately NOT noRouteMessage: with
// master_only the gateway may be perfectly healthy and busy on three other
// sessions, so "no connected session available" would send an operator hunting
// a gateway outage that is not happening. It names the real, fixable cause —
// the master handset is offline — because that is the only thing that will
// make these sends work again.
const noMasterRouteMessage = "no connected master session (master_only was set; " +
	"the master handset is offline — reconnect it, or clear master_only to route " +
	"through any connected session)"

// noAutoRouteMessage builds the 409 body for every automatic-routing send
// endpoint (PostAPISendMessageAuto, PostAPISendGroupMessage,
// PostAPISendMediaAuto, PostAPIContactsCheckAuto). It is a PURE function
// over the caller's own snapshot list plus the request's allow_master flag
// — no Manager call inside it — so it is unit-testable without a live
// session.
//
// The plain noRouteMessage is returned unchanged in two cases: a genuinely
// empty/disconnected gateway (must still read as an outage, not a master
// puzzle) and allowMaster == true (the caller already opted in, so a master
// being excluded is not what happened). Only when snaps contains at least
// one Connected && IsMaster entry AND allowMaster is false does the hint
// get appended — that is the one case where the plain message would send an
// operator hunting a phantom outage instead of the real, fixable cause.
func noAutoRouteMessage(snaps []wa.Snapshot, allowMaster bool) string {
	if allowMaster {
		return noRouteMessage
	}
	for _, snap := range snaps {
		if snap.Connected && snap.IsMaster {
			return noRouteMessage + masterExclusionHint
		}
	}
	return noRouteMessage
}

// PostAPILogin authenticates an API client and returns a bearer access token.
func (h *handlers) PostAPILogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	loginID := strings.TrimSpace(req.LoginID)
	secret := req.Secret
	if loginID == "" && strings.TrimSpace(req.Username) != "" {
		loginID = strings.TrimSpace(req.Username)
		secret = req.Password
	}
	issued, err := h.APIClients.Login(r.Context(), loginID, secret)
	if err != nil {
		if errors.Is(err, apiclient.ErrInactiveClient) {
			writeError(w, http.StatusUnauthorized, "api client is inactive")
			return
		}
		if errors.Is(err, apiclient.ErrExpiredClient) {
			writeError(w, http.StatusUnauthorized, "api client access period has ended")
			return
		}
		if errors.Is(err, apiclient.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid login id or secret")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	var expires *string
	if issued.ExpiresAt != nil {
		v := issued.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		expires = &v
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token:     issued.Token,
		ExpiresAt: expires,
	})
}

// PostAPILogout revokes the current API-client bearer token.
// PostAPILogout revokes the bearer token that authenticated this request.
//
// It is meaningful ONLY on the bearer path. A caller authenticated with HTTP
// Basic minted no token, so there is nothing to revoke — answering 204 there
// would report success for work that did not happen, and leave someone
// believing they had cut off access they still have. Revoking Basic access
// means deactivating the client or rotating its secret.
func (h *handlers) PostAPILogout(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.APIPrincipalFrom(r.Context())
	if principal.TokenHash == "" {
		writeError(w, http.StatusBadRequest,
			"logout applies to bearer tokens only; a Basic-authenticated client has no token to revoke")
		return
	}
	_ = h.APIClients.RevokeToken(r.Context(), principal.TokenHash)
	w.WriteHeader(http.StatusNoContent)
}

// GetStats returns the dashboard KPI values as JSON.
func (h *handlers) GetStats(w http.ResponseWriter, r *http.Request) {
	widgets := h.buildWidgets(r.Context())
	out := make(map[string]string, len(widgets))
	for _, wd := range widgets {
		out[wd.Label] = wd.Value
	}
	writeJSON(w, http.StatusOK, out)
}

// GetHealth reports process health with real session counts. No DB ping
// needed — SQLite is local to the process.
//
// backend, when the active backend supports GatewayHealth (waxum), reports
// its CACHED reachability verdict (Manager.CachedBackendHealth) — never a
// live call: this route is unauthenticated and is the container healthcheck
// (docker-compose.yml), probed on every liveness interval, and an inline
// call there would put a network hop on every probe and feed waxum's own
// rate limiter for no reason a liveness check needs (CLAUDE.md's waxum
// rate-limit hazard note). "reachable" is omitted (not defaulted to
// true/false) when the event pump maintaining that cache hasn't recorded a
// first verdict yet (cold boot, or WSM_WAXUM_EVENTS=false) — an omitted
// field reads as "unknown", not as a guess. The field itself is omitted
// entirely under a backend with no GatewayHealth at all (whatsmeow).
func (h *handlers) GetHealth(w http.ResponseWriter, r *http.Request) {
	total, connected := h.Manager.Counts()
	out := map[string]any{
		"status":    "ok",
		"sessions":  total,
		"connected": connected,
	}
	if name, reachable, known, supported := h.Manager.CachedBackendHealth(); supported {
		backend := map[string]any{"name": name}
		if known {
			backend["reachable"] = reachable
		}
		out["backend"] = backend
	}
	writeJSON(w, http.StatusOK, out)
}
