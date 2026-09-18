// known_groups_ui.go: a small per-session CRUD mapping a friendly name to a
// real WhatsApp group JID (internal/domain/knowngroup), fed by the existing
// live GET /api/v1/sessions/{id}/groups endpoint (see api_messages_group.go's
// GetAPISessionGroups). Follows self_convo_templates_ui.go's shape: the rows
// table is an HTMX partial rendered at 2xx no matter the outcome.
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/knowngroup"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// knownGroupsPageData drives GET /groups.
type knownGroupsPageData struct {
	PageBase
	Sessions []wa.Snapshot
}

// GetKnownGroups renders the /groups CRUD page. Never gated by any feature
// flag — managing the picker source is always safe; only Group Broadcast
// Schedule's own mutating endpoints are gated.
func (h *handlers) GetKnownGroups(w http.ResponseWriter, r *http.Request) {
	data := knownGroupsPageData{Sessions: h.scheduleSessions()}
	data.PageBase = h.newPageBase(r, "Known Groups", "/groups")
	h.Renderer.RenderPage(w, "known_groups", data)
}

// knownGroupRowsData feeds the known_group_rows partial.
type knownGroupRowsData struct {
	Error  string
	Groups []knowngroup.Group
}

// knownGroups lists every known group across every session, newest first.
// h.KnownGroups == nil renders an empty list, the same nil-guard
// selfConvoSchedules (self_convo_schedule_ui.go) uses.
func (h *handlers) knownGroups(r *http.Request) []knowngroup.Group {
	if h.KnownGroups == nil {
		return nil
	}
	groups, err := h.KnownGroups.List(r.Context())
	if err != nil {
		return nil
	}
	return groups
}

func (h *handlers) knownGroupRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "known_group_rows", knownGroupRowsData{Error: errMsg, Groups: h.knownGroups(r)})
}

// GetKnownGroupRows is the list page's poll target.
func (h *handlers) GetKnownGroupRows(w http.ResponseWriter, r *http.Request) {
	h.knownGroupRowsWithError(w, r, "")
}

// PostKnownGroupCreate is POST /groups — creates a known group for a
// session from an operator-supplied name plus either a pasted JID or one
// selected from the live joined-groups picker (GetKnownGroupPicker).
func (h *handlers) PostKnownGroupCreate(w http.ResponseWriter, r *http.Request) {
	if h.KnownGroups == nil {
		h.knownGroupRowsWithError(w, r, "known groups are not available on this deployment")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.knownGroupRowsWithError(w, r, "invalid form submission")
		return
	}
	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	rawJID := strings.TrimSpace(r.FormValue("group_jid"))

	if sessionID == "" {
		h.knownGroupRowsWithError(w, r, "choose a session")
		return
	}
	if _, ok := h.Manager.Get(sessionID); !ok {
		h.knownGroupRowsWithError(w, r, "unknown session")
		return
	}
	if name == "" {
		h.knownGroupRowsWithError(w, r, "name is required")
		return
	}
	jid, err := wa.ParseGroupJID(rawJID)
	if err != nil {
		h.knownGroupRowsWithError(w, r, "not a valid WhatsApp group JID")
		return
	}

	g := &knowngroup.Group{ID: wa.NewID(), SessionID: sessionID, GroupJID: jid.String(), Name: name}
	if err := h.KnownGroups.Insert(r.Context(), g); err != nil {
		msg := err.Error()
		if errors.Is(err, knowngroup.ErrDuplicate) {
			msg = "this session already has that group saved"
		}
		h.knownGroupRowsWithError(w, r, msg)
		return
	}
	h.GetKnownGroupRows(w, r)
}

// PostKnownGroupDelete is POST /groups/{id}/delete.
func (h *handlers) PostKnownGroupDelete(w http.ResponseWriter, r *http.Request) {
	if h.KnownGroups == nil {
		h.knownGroupRowsWithError(w, r, "known groups are not available on this deployment")
		return
	}
	if err := h.KnownGroups.Delete(r.Context(), r.PathValue("id")); err != nil {
		msg := err.Error()
		if errors.Is(err, knowngroup.ErrNotFound) {
			msg = "group not found"
		}
		h.knownGroupRowsWithError(w, r, msg)
		return
	}
	h.GetKnownGroupRows(w, r)
}

// knownGroupPickerData feeds the known_group_picker partial — the live
// joined-groups list for one session, so an operator can tick one instead
// of pasting a JID by hand.
type knownGroupPickerData struct {
	SessionID string
	Groups    []wa.GroupInfo
	Error     string
}

// GetKnownGroupPicker is GET /groups/picker?session_id=... — an HTMX
// partial swap fed by the SAME live GET /api/v1/sessions/{id}/groups path
// GetAPISessionGroups uses (api_messages_group.go), called here directly
// against the session rather than through the HTTP API.
func (h *handlers) GetKnownGroupPicker(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	data := knownGroupPickerData{SessionID: sessionID}
	if sessionID == "" {
		h.Renderer.RenderPartial(w, "known_group_picker", data)
		return
	}
	s, ok := h.Manager.Get(sessionID)
	if !ok {
		data.Error = "unknown session"
		h.Renderer.RenderPartial(w, "known_group_picker", data)
		return
	}
	groups, err := s.JoinedGroups(r.Context())
	if err != nil {
		if errors.Is(err, wa.ErrNotConnected) {
			data.Error = "session not connected"
		} else {
			data.Error = "could not list this session's groups"
		}
		h.Renderer.RenderPartial(w, "known_group_picker", data)
		return
	}
	data.Groups = groups
	h.Renderer.RenderPartial(w, "known_group_picker", data)
}
