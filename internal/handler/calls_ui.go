package handler

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// callsRecentLimitPerSession/callsRecentLimitTotal bound the fleet-wide
// merge recentCalls performs below — see that function's doc comment for
// why a merge is needed at all (there is no fleet-level call.Repository
// query, only a per-session one).
const (
	callsRecentLimitPerSession = 10
	callsRecentLimitTotal      = 30
)

// callsPageData drives GET /calls. Connected mirrors sendPageData's own
// field (send.go) exactly — the same "Automatic routing" rule the /send
// page uses applies here too, via the shared selectSendSession helper.
type callsPageData struct {
	PageBase
	CallsEnabled bool
	Connected    []wa.Snapshot
}

// callRowView is one wa_calls row plus the session name call.Call itself
// does not carry — mirrors apiCall's (api_calls.go) reason for existing as
// its own type, one layer up (a template view, not a JSON view).
type callRowView struct {
	call.Call
	SessionName string
}

// callRowsData feeds the call_rows partial — the SAME partial both the 5s
// poll (GetCallRows) and every place/terminate action render, per
// CLAUDE.md's "the polled partial and the action-response partial are the
// same partial" rule. Error is set only by an action's own validation
// failure; the 5s poll always renders it empty.
type callRowsData struct {
	CallsEnabled bool
	Error        string
	Calls        []callRowView
}

// GetCalls renders the /calls page. The place-call form and its degraded
// message live in the template, keyed on CallsEnabled/Connected — this
// handler never 500s regardless of either being false/empty, matching the
// phase brief's "the page renders and explains, the button does not lie."
func (h *handlers) GetCalls(w http.ResponseWriter, r *http.Request) {
	var connected []wa.Snapshot
	for _, s := range h.Manager.Snapshots() {
		if s.Connected {
			connected = append(connected, s)
		}
	}
	data := callsPageData{CallsEnabled: h.CallsEnabled, Connected: connected}
	data.PageBase = h.newPageBase(r, "Voice Calls", "/calls")
	h.Renderer.RenderPage(w, "calls", data)
}

// recentCalls fans out call.Repository.Recent across every known session
// (there is no fleet-level equivalent — GetAPISessionCalls, api_calls.go,
// is scoped the same way) and merges by CreatedAt, capped at
// callsRecentLimitTotal. h.Calls is nil in a handful of older test servers
// that predate Phase 6 (newLogsTestServer) — guarded the same way
// h.Objects is elsewhere, rather than requiring every such test to grow a
// call.Repository it does not otherwise need.
func (h *handlers) recentCalls(r *http.Request) []callRowView {
	if h.Calls == nil {
		return nil
	}
	names := h.sessionNames()
	var out []callRowView
	for _, s := range h.Manager.Snapshots() {
		rows, err := h.Calls.Recent(r.Context(), s.ID, callsRecentLimitPerSession)
		if err != nil {
			slog.Warn("recent calls: could not read wa_calls", "session", s.ID, "err", err)
			continue
		}
		for _, c := range rows {
			out = append(out, callRowView{Call: c, SessionName: names.label(s.ID)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > callsRecentLimitTotal {
		out = out[:callsRecentLimitTotal]
	}
	return out
}

// callRowsWithError renders call_rows with an optional inline error banner
// — the mechanism every validation failure in this file uses to surface
// its message, since there is no separate call-result partial (unlike
// send_result for /send): the phase brief lists exactly four new partials
// and call_result is not one of them.
func (h *handlers) callRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "call_rows", callRowsData{
		CallsEnabled: h.CallsEnabled, Error: errMsg, Calls: h.recentCalls(r),
	})
}

// GetCallRows is the 5s poll target (calls.html's tbody hx-trigger,
// mirroring sessions/list.html) — never carries an error.
func (h *handlers) GetCallRows(w http.ResponseWriter, r *http.Request) {
	h.callRowsWithError(w, r, "")
}

// callsGuardUI mirrors api_calls.go's callsGuard for the browser surface:
// every POST /calls/* route calls this first. Unlike the API's 501, the UI
// has nothing structured to return — it re-renders call_rows with the same
// disabled message so a direct POST (curl, or a stale page) degrades the
// same way the page itself already does.
func (h *handlers) callsGuardUI(w http.ResponseWriter, r *http.Request) bool {
	if h.CallsEnabled {
		return true
	}
	h.callRowsWithError(w, r, callsDisabledMessage)
	return false
}

// placeCallUI mirrors placeCall's (api_calls.go) insert-then-place-then-
// Mark* sequence, rendering call_rows instead of a JSON body.
func (h *handlers) placeCallUI(w http.ResponseWriter, r *http.Request, c *call.Call, place func() (wa.CallOutcome, error)) {
	if p, ok := auth.APIPrincipalFrom(r.Context()); ok {
		c.APIClientID = p.ClientID
	}
	if err := h.Calls.Insert(r.Context(), c); err != nil {
		h.callRowsWithError(w, r, "could not record the call")
		return
	}
	outcome, err := place()
	if err != nil {
		_ = h.Calls.MarkFailed(r.Context(), c.ID, err.Error())
		h.callRowsWithError(w, r, err.Error())
		return
	}
	_ = h.Calls.MarkPlaced(r.Context(), c.ID, outcome.CallID, outcome.RecordingURL)
	h.GetCallRows(w, r)
}

// PostCallsTTS is POST /calls/tts — the browser form's sibling of
// PostAPICallTTS. voice defaults to waxum's own DefaultTTSVoice when empty,
// same as the API path.
func (h *handlers) PostCallsTTS(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuardUI(w, r) {
		return
	}
	s, errMsg := h.selectSendSession(r)
	if errMsg != "" {
		h.callRowsWithError(w, r, errMsg)
		return
	}
	digits, err := wa.NormalizePhone(r.FormValue("to"))
	if err != nil {
		h.callRowsWithError(w, r, err.Error())
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		h.callRowsWithError(w, r, "text is required")
		return
	}
	voice := strings.TrimSpace(r.FormValue("voice"))
	c := &call.Call{ID: wa.NewID(), SessionID: s.ID, ToPhone: digits, Kind: call.KindTTS,
		Text: text, Voice: voice, Status: call.StatusPlacing, CreatedAt: time.Now().UTC()}
	h.placeCallUI(w, r, c, func() (wa.CallOutcome, error) {
		return s.PlaceCallTTS(r.Context(), wa.ToJID(digits), text, voice)
	})
}

// PostCallsPlay is POST /calls/play — the browser form's sibling of
// PostAPICallPlay. See that handler's doc comment: waxum's SSRF guard
// rejects any private audio_url, so this can never point inside the
// compose network.
func (h *handlers) PostCallsPlay(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuardUI(w, r) {
		return
	}
	s, errMsg := h.selectSendSession(r)
	if errMsg != "" {
		h.callRowsWithError(w, r, errMsg)
		return
	}
	digits, err := wa.NormalizePhone(r.FormValue("to"))
	if err != nil {
		h.callRowsWithError(w, r, err.Error())
		return
	}
	audioURL := strings.TrimSpace(r.FormValue("audio_url"))
	if audioURL == "" {
		h.callRowsWithError(w, r, "audio_url is required")
		return
	}
	c := &call.Call{ID: wa.NewID(), SessionID: s.ID, ToPhone: digits, Kind: call.KindPlay,
		AudioURL: audioURL, Status: call.StatusPlacing, CreatedAt: time.Now().UTC()}
	h.placeCallUI(w, r, c, func() (wa.CallOutcome, error) {
		return s.PlaceCallPlay(r.Context(), wa.ToJID(digits), audioURL)
	})
}

// PostCallsTerminate is POST /calls/{id}/terminate — {id} is the SESSION
// id (the URL this route is under has no per-call id in it; call_id/peer
// arrive as form fields from the row's own terminate button, mirroring
// PostAPICallTerminate's {peer, call_id, reason} body).
func (h *handlers) PostCallsTerminate(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuardUI(w, r) {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		h.callRowsWithError(w, r, "session not found")
		return
	}
	digits, err := wa.NormalizePhone(r.FormValue("peer"))
	if err != nil {
		h.callRowsWithError(w, r, err.Error())
		return
	}
	callID := strings.TrimSpace(r.FormValue("call_id"))
	if callID == "" {
		h.callRowsWithError(w, r, "call_id is required")
		return
	}
	if err := s.TerminateCall(r.Context(), wa.ToJID(digits), callID, r.FormValue("reason")); err != nil {
		h.callRowsWithError(w, r, err.Error())
		return
	}
	if err := h.Calls.MarkTerminatedByCallID(r.Context(), s.ID, callID); err != nil {
		slog.Warn("call terminate: could not mark row terminated (best-effort)", "session", s.ID, "call_id", callID, "err", err)
	}
	h.GetCallRows(w, r)
}
