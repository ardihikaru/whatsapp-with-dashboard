package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

// writeWAError maps a wa.Err* sentinel (or a *wa.ValidationError, matched
// first via errors.As since it is a struct type, not a sentinel) to its HTTP
// status and writes it. internal/wa/errors.go's header comment documents
// this exact map — keep the two in step whenever either changes.
//
// Extracted from apiToggle (previously the only hand-rolled copy of this
// switch, covering ErrNotPaired/ErrDeviceGone/ErrBackendBusy) as the FIRST
// thing Phase 6a does, precisely because that phase adds 20+ endpoints that
// all need the same mapping: a switch statement copy-pasted 20 times is how
// this status map rots the moment one sentinel's meaning changes and only
// some copies get updated. Every handler this package adds from here on
// funnels its wa.* errors through this one function instead.
func writeWAError(w http.ResponseWriter, err error) {
	var verr *wa.ValidationError
	switch {
	case err == nil:
		return
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, verr.Msg)
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

// selectForSend picks the session for an automatic-routing send. It is the
// single delegation to wa.Manager.SelectForRoute every auto-routing endpoint
// goes through — the actual MODE precedence (group scope vs ungrouped,
// master_only vs allow_master) now lives entirely in wa.Route and
// wa.EligibleForRoute (internal/wa/load_balance.go), not here. This function
// stays the single grep target for "where HTTP chooses a route" even though
// it no longer branches on mode itself; ALL FOUR auto-routing endpoints
// (PostAPISendMessageAuto, PostAPISendGroupMessage, PostAPISendMediaAuto,
// PostAPIContactsCheckAuto) call it.
func selectForSend(r *http.Request, m *wa.Manager, rt wa.Route) (*wa.ManagedSession, wa.Snapshot, bool) {
	return m.SelectForRoute(r.Context(), rt)
}

// noSendRouteMessage builds the 409 body for whichever scope/mode
// selectForSend was asked for. Pure, like noAutoRouteMessage — no Manager
// call, just the caller's own snapshot list — so every branch stays
// unit-testable without a live session. The ungrouped paths (rt.GroupCode
// == "") are BYTE-IDENTICAL to what this returned before group_code
// existed: see noRouteMessage/masterExclusionHint/noMasterRouteMessage's own
// doc comments below, which this never rewrites.
func noSendRouteMessage(snaps []wa.Snapshot, rt wa.Route) string {
	if rt.GroupCode != "" {
		return noGroupRouteMessage(snaps, rt)
	}
	if rt.MasterOnly {
		return noMasterRouteMessage
	}
	return noAutoRouteMessage(snaps, rt.AllowMaster)
}

// Hint fragments noGroupRouteMessage appends to its base message — kept as
// named constants, mirroring masterExclusionHint above, so each is checkable
// as its own literal rather than only as a shape.
const (
	// groupNoAssignmentHint fires when NO snapshot at all carries the
	// requested group_code — the most common real mistake (a typo, or a
	// session moved out of the group) and the one most likely to otherwise
	// read as a mysterious outage.
	groupNoAssignmentHint = "no session is currently assigned to this group_code — check the spelling, or the Group column on /sessions"
	// groupMasterOnlyExclusionHint fires when master_only is set and the
	// group has a connected session, just not one flagged master.
	groupMasterOnlyExclusionHint = "sessions in this group are connected, but none is flagged master — flag one master, or clear master_only"
	// groupMasterExclusionHint is masterExclusionHint's scoped twin: the
	// group's only connected session(s) are masters, excluded because
	// allow_master was not set.
	groupMasterExclusionHint = "the only connected session(s) in this group are master sessions; set allow_master to route through one"
	// groupOutsideConnectedHint fires when the gateway overall is healthy —
	// something else, outside this group_code, is connected — so the
	// operator should not go looking for a gateway outage.
	groupOutsideConnectedHint = "the gateway is not down — other sessions are connected outside this group_code"
	// groupRouteEscapeHatch is appended to EVERY group-scoped 409, always
	// last, so an on-call reader who has never heard of group_code can
	// still get a message out without learning the feature first.
	groupRouteEscapeHatch = "omit group_code to route through any connected session"
)

// groupRouteBaseMessage is noGroupRouteMessage's base sentence — deliberately
// NOT built from noRouteMessage (see that constant's own doc comment): a
// group-scoped 409 must never equal or be prefixed by the plain "no
// connected session available" text, because with a group code the gateway
// may be perfectly healthy and busy on other groups, and TestNoSendRouteMessageNamesTheGroupAndNotAGatewayOutage
// pins exactly that distinction.
func groupRouteBaseMessage(rt wa.Route) string {
	if rt.MasterOnly {
		// "master_only was set" belongs in the SENTENCE, not a parenthetical
		// of its own: groupRouteMessage appends one more, and three stacked
		// parentheses is the point an on-call reader stops reading.
		return fmt.Sprintf("master_only was set, but no connected master session exists for group_code %q", rt.GroupCode)
	}
	return fmt.Sprintf("no connected session for group_code %q", rt.GroupCode)
}

// noGroupRouteMessage is noSendRouteMessage's group-scoped half — one of the
// two axes that changed, group_code, meets the other, master_only/
// allow_master. It picks by the FIRST matching rule below, evidence-only
// (over the caller's own snapshot list, no Manager call), exactly like
// noAutoRouteMessage's own discipline:
//
//  1. no snapshot carries rt.GroupCode at all — nothing is assigned to the
//     named group, most likely a typo or a session moved out of it;
//  2. rt.MasterOnly and the group HAS a connected non-master — the group is
//     up, just not flagged master;
//  3. not MasterOnly, not AllowMaster, and the group has a connected
//     master — the scoped twin of noAutoRouteMessage's masterExclusionHint;
//  4. some snapshot OUTSIDE the group is connected — the gateway itself is
//     not down;
//  5. otherwise — the base message alone, because a genuinely dark gateway
//     must still read as an outage. This is the direct analogue of
//     noAutoRouteMessage's own refusal to append a hint to an empty
//     gateway, and the rule most easily got wrong.
func noGroupRouteMessage(snaps []wa.Snapshot, rt wa.Route) string {
	base := groupRouteBaseMessage(rt)

	var groupHasSnapshot, groupHasConnectedNonMaster, groupHasConnectedMaster, outsideGroupConnected bool
	for _, snap := range snaps {
		if snap.InGroupScope(rt.GroupCode) {
			groupHasSnapshot = true
			if snap.Connected {
				if snap.IsMaster {
					groupHasConnectedMaster = true
				} else {
					groupHasConnectedNonMaster = true
				}
			}
		} else if snap.Connected {
			outsideGroupConnected = true
		}
	}

	switch {
	case !groupHasSnapshot:
		return groupRouteMessage(base, groupNoAssignmentHint)
	case rt.MasterOnly && groupHasConnectedNonMaster:
		return groupRouteMessage(base, groupMasterOnlyExclusionHint)
	case !rt.MasterOnly && !rt.AllowMaster && groupHasConnectedMaster:
		return groupRouteMessage(base, groupMasterExclusionHint)
	case outsideGroupConnected:
		return groupRouteMessage(base, groupOutsideConnectedHint)
	default:
		return groupRouteMessage(base, "")
	}
}

// groupRouteMessage assembles one group-scoped 409 body. The hints above and
// the escape hatch share a SINGLE parenthetical rather than sitting in two
// adjacent ones: an on-call reader parses "…(cause; way out)" at a glance and
// stumbles over "…(cause) (way out)", and stumbling is the whole thing these
// messages exist to prevent. The escape hatch is always last, so it reads as
// the closing instruction whether or not a cause was identifiable.
func groupRouteMessage(base, hint string) string {
	if hint == "" {
		return base + " (" + groupRouteEscapeHatch + ")"
	}
	return base + " (" + hint + "; " + groupRouteEscapeHatch + ")"
}

// routingMode names which routing decision produced the session behind a
// send response's "routing" field (postAPISendThrough, sendMedia). A DEFINED
// type, not a plain string: postAPISendThrough already takes several
// adjacent string parameters (toDisplay, text) and one more untyped string
// would be transposable with no compiler error to catch it.
type routingMode string

const (
	routingAuto            routingMode = "auto"
	routingMasterOnly      routingMode = "master_only"
	routingGroup           routingMode = "group"
	routingGroupMasterOnly routingMode = "group_master_only"
	// routingExplicit is reported by every route that names its own session
	// id (PostAPISendMessage, PostAPISessionSendMedia) — there is no
	// Route/EligibleForRoute pick to describe, but the response's "routing"
	// key stays present regardless, so a caller never has to special-case
	// "field absent" as one more meaning.
	routingExplicit routingMode = "explicit"
)

// routingModeFor derives the reported mode from the Route an automatic pick
// was made against.
func routingModeFor(rt wa.Route) routingMode {
	switch {
	case rt.GroupCode != "" && rt.MasterOnly:
		return routingGroupMasterOnly
	case rt.GroupCode != "":
		return routingGroup
	case rt.MasterOnly:
		return routingMasterOnly
	default:
		return routingAuto
	}
}

// sendResponseFields builds the three routing-decision keys every send/media
// success response carries: session_group_code (always present, "" when
// ungrouped — an ABSENT key would be indistinguishable from an older
// gateway that never had group_code at all), session_is_master, and
// routing. Factored out as its own pure function, the same way
// noSendRouteMessage sits beside selectForSend, so it is directly
// unit-testable — dispatchSend's own doc comment (send.go) explains why
// package handler cannot otherwise reach the success branch of a real send
// to exercise this inline. Both postAPISendThrough (api_messages.go) and
// sendMedia (api_media.go) merge this into their own response map.
func sendResponseFields(snap wa.Snapshot, mode routingMode) map[string]any {
	return map[string]any{
		"session_group_code": snap.GroupCode,
		"session_is_master":  snap.IsMaster,
		"routing":            string(mode),
	}
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
	// reconcile: COUNTS AND BOOLEANS ONLY. This route is unauthenticated and
	// published to the internet by the edge allowlist, so no session id,
	// name or state may appear here — the detail lives on the authenticated
	// /sessions page and the system log. Omitted when no reconciler runs
	// (an omitted field reads "nobody is checking", not "all clear").
	if rep := h.Manager.Reconcile(); rep.Enabled {
		rec := map[string]any{
			"ok":              rep.OK,
			"mismatches":      len(rep.Mismatches),
			"backend_problem": rep.BackendProblem != nil,
		}
		if !rep.CheckedAt.IsZero() {
			rec["last_checked"] = rep.CheckedAt.UTC().Format(time.RFC3339)
		}
		out["reconcile"] = rec
	}
	writeJSON(w, http.StatusOK, out)
}
