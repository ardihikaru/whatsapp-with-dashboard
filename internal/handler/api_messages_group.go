package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// PostAPISendGroupMessage sends to a WhatsApp GROUP.
//
//	POST /api/v1/messages/group  {"group_jid": "...@g.us", "text": "..."}
//
// Its status vocabulary is IDENTICAL to PostAPISendMessageAuto's — 200 sent/failed,
// 202 still queued after 15s, 400 validation, 409 not connected, 429 queue full.
// That symmetry is deliberate: the alerter picks between this endpoint and the
// per-number one at runtime, and it must not have to learn two error dialects to
// report one failure.
//
// This is the endpoint deploy/production/netdata/wa-notify.sh was written against
// months before it existed.
func (h *handlers) PostAPISendGroupMessage(w http.ResponseWriter, r *http.Request) {
	req, jid, ok := decodeGroupSendRequest(w, r)
	if !ok {
		return
	}
	s, _, ok := selectForSend(r, h.Manager, req.Route)
	if !ok {
		writeError(w, http.StatusConflict, noSendRouteMessage(h.Manager.Snapshots(), req.Route))
		return
	}
	// Group sends are alerts by default (the alerter's WA_ALERT_GROUP_JID path).
	attr := message.Attribution{
		ClientRef: strings.TrimSpace(req.ClientRef),
		TenantRef: strings.TrimSpace(req.TenantRef),
		Category:  message.NormalizeCategory(req.Category, message.CategoryAlert),
	}
	h.postAPISendThrough(w, r, s, jid, jid.String(), req.Text, attr, routingModeFor(req.Route))
}

// groupSendRequest is decodeGroupSendRequest's parsed body. Kept as its OWN
// struct rather than sharing textSendRequest (api_messages.go): a group
// send has a group_jid, not a phone number, and no Digits field at all —
// forcing it into the phone-send shape would leave a field that is never
// meaningful for this endpoint.
//
// GroupJID and GroupCode are unrelated despite the shared word: GroupJID is
// a WhatsApp CHAT identifier (an @g.us conversation this session belongs
// to); GroupCode is a WSM ROUTING CLUSTER (wa.Route.GroupCode, an operator
// label with no WhatsApp meaning at all). This is the one endpoint where
// both words appear in a single body — read each field's own comment before
// touching either.
type groupSendRequest struct {
	// GroupJID names a WhatsApp GROUP CHAT — see the struct doc comment.
	GroupJID  string `json:"group_jid"`
	Text      string `json:"text"`
	ClientRef string `json:"client_ref"`
	TenantRef string `json:"tenant_ref"`
	Category  string `json:"category"`
	// AllowMaster/MasterOnly/GroupCode are the raw wire fields; Route below
	// is what every call site actually uses (see wa.Route's precedence
	// rules — master_only OVERRIDES allow_master rather than combining with
	// it).
	AllowMaster bool `json:"allow_master"`
	MasterOnly  bool `json:"master_only"`
	// GroupCode is a WSM ROUTING CLUSTER — see the struct doc comment.
	GroupCode string `json:"group_code"`
	// Route is Group/AllowMaster/MasterOnly folded into one wa.Route, built
	// after normalization in decodeGroupSendRequest.
	Route wa.Route `json:"-"`
}

// decodeGroupSendRequest reads and validates the group-send body, returning
// the already-parsed group JID alongside it so the one call site does not
// have to parse it twice. Extracted from PostAPISendGroupMessage purely so
// the allow_master flag has a unit-testable seam of its own, matching
// decodeSendRequest/decodeContactsCheckRequest's shape.
func decodeGroupSendRequest(w http.ResponseWriter, r *http.Request) (groupSendRequest, wa.JID, bool) {
	var req groupSendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return groupSendRequest{}, wa.JID{}, false
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > maxTextChars {
		writeError(w, http.StatusBadRequest, "text is required, max 4096 characters")
		return groupSendRequest{}, wa.JID{}, false
	}
	jid, err := wa.ParseGroupJID(req.GroupJID)
	if err != nil {
		// Specifically catches a user JID sent here by mistake. Delivering that
		// to a private chat and returning 200 would be the worst outcome: an
		// alert that reports success and reaches nobody who is on call.
		writeError(w, http.StatusBadRequest, err.Error())
		return groupSendRequest{}, wa.JID{}, false
	}
	groupCode, err := wa.NormalizeGroupCode(req.GroupCode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return groupSendRequest{}, wa.JID{}, false
	}
	req.Route = wa.Route{GroupCode: groupCode, AllowMaster: req.AllowMaster, MasterOnly: req.MasterOnly}
	return req, jid, true
}

// GetAPISessionGroups lists the groups this device belongs to, so an operator can
// discover WA_ALERT_GROUP_JID. A group JID is not guessable and WhatsApp shows it
// nowhere in the app.
//
// One endpoint, no UI: whoever configures the alerter already holds a bearer token
// (the alerter needs one), so this is a single curl. A group picker in the send
// page is a reasonable follow-up; a CLI is not — it would need its own whatsmeow
// client, and a second client on the same store is exactly what the singleton rule
// forbids.
func (h *handlers) GetAPISessionGroups(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	groups, err := s.JoinedGroups(r.Context())
	if err != nil {
		if errors.Is(err, wa.ErrNotConnected) {
			writeError(w, http.StatusConflict, "session not connected")
			return
		}
		writeError(w, http.StatusBadGateway, "could not list groups")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
