// api_contacts_crud.go: the JSON API over the owner-managed WhatsApp
// contact book (internal/domain/contact) — GET/POST /api/v1/contacts,
// DELETE/POST /api/v1/contacts/{contactID}[/verify]. Deliberately NOT named
// "{id}" in routes_api.go: wsm-mcp's generic routeSpec (internal/mcp/
// registry/t_wsm_routes.go) treats any "{id}" segment as a SESSION id to
// resolve via session_id/phone, and a contact id is not one — see that
// file's own comment on containsPathVar("id").
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/contactmatch"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// apiContact is the wire shape of one contact, shared by every route in
// this file. Aliases travels as an array on the wire even though it is
// stored as one comma-joined column — the same "one storage shape, a
// friendlier wire shape" split checkContacts' contactVerdict does not need
// but this table's own aliasesToWire/wireToAliases below does.
type apiContact struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Phone        string   `json:"phone"`
	Aliases      []string `json:"aliases"`
	JID          string   `json:"jid,omitempty"`
	WAStatus     string   `json:"wa_status"`
	VerifiedAt   string   `json:"verified_at,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	MatchScore   int      `json:"match_score,omitempty"`
	MatchedField string   `json:"matched_field,omitempty"`
}

func aliasesToWire(raw string) []string {
	if raw == "" {
		return []string{}
	}
	return strings.Split(raw, ",")
}

func wireFromContact(c contact.Contact) apiContact {
	out := apiContact{
		ID: c.ID, Name: c.Name, Phone: c.Phone, Aliases: aliasesToWire(c.Aliases),
		JID: c.JID, WAStatus: string(c.WAStatus),
		CreatedAt: c.CreatedAt.Format(time.RFC3339), UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
	if !c.VerifiedAt.IsZero() {
		out.VerifiedAt = c.VerifiedAt.Format(time.RFC3339)
	}
	return out
}

// contactsOr503 reports whether Deps.Contacts is wired, writing a 503 when
// not — same shape as webhookAdminOr501 but for an always-present-in-
// production dependency a test server may simply not have wired.
func (h *handlers) contactsOr503(w http.ResponseWriter) bool {
	if h.Contacts == nil {
		writeError(w, http.StatusServiceUnavailable, "contacts are not available on this deployment")
		return false
	}
	return true
}

// contactMatchFields builds this contact's searchable fields for
// contactmatch.Match: its name plus one Field per alias.
func contactMatchFields(c contact.Contact) []contactmatch.Field {
	fields := []contactmatch.Field{{Name: "name", Value: c.Name}}
	for _, a := range aliasesToWire(c.Aliases) {
		if a == "" {
			continue
		}
		fields = append(fields, contactmatch.Field{Name: "alias", Value: a, Alias: true})
	}
	return fields
}

// rankedMatch is one contact plus its contactmatch.Result, kept together
// for the sort below.
type rankedMatch struct {
	contact.Contact
	contactmatch.Result
}

// GetAPIContacts is GET /api/v1/contacts — every saved contact, or (with
// ?q=) ranked by contactmatch over name+aliases, highest score first; ties
// break by name for a stable order.
func (h *handlers) GetAPIContacts(w http.ResponseWriter, r *http.Request) {
	if !h.contactsOr503(w) {
		return
	}
	all, err := h.Contacts.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		out := make([]apiContact, 0, len(all))
		for _, c := range all {
			out = append(out, wireFromContact(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"contacts": out, "total": len(out)})
		return
	}
	var ranked []rankedMatch
	for _, c := range all {
		res, ok := contactmatch.Match(q, contactMatchFields(c))
		if !ok {
			continue
		}
		ranked = append(ranked, rankedMatch{Contact: c, Result: res})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Name < ranked[j].Name
	})
	out := make([]apiContact, 0, len(ranked))
	for _, rm := range ranked {
		w := wireFromContact(rm.Contact)
		w.MatchScore = rm.Score
		w.MatchedField = rm.Field
		out = append(out, w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": out, "total": len(out), "query": q})
}

// contactCreateRequest is POST /api/v1/contacts' body.
type contactCreateRequest struct {
	Name    string   `json:"name"`
	Phone   string   `json:"phone"`
	Aliases []string `json:"aliases"`
}

// PostAPIContacts is POST /api/v1/contacts — creates a contact and
// immediately verifies it through an auto-routed connected session, the
// same checkContacts path contacts_ui.go's verifyContact uses. A verify
// failure never fails the create (the contact is saved either way); its
// status is simply left/left-at StatusUnknown and the response names the
// verify problem in "verify_error" alongside the 201.
func (h *handlers) PostAPIContacts(w http.ResponseWriter, r *http.Request) {
	if !h.contactsOr503(w) {
		return
	}
	var req contactCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	digits, err := wa.NormalizePhone(req.Phone)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	aliases := make([]string, 0, len(req.Aliases))
	for _, a := range req.Aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			aliases = append(aliases, a)
		}
	}

	c := &contact.Contact{ID: wa.NewID(), Name: name, Phone: digits, Aliases: strings.Join(aliases, ",")}
	if err := h.Contacts.Insert(r.Context(), c); err != nil {
		if errors.Is(err, contact.ErrDuplicate) {
			writeError(w, http.StatusConflict, "a contact with this phone number already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	verifyErr := h.verifyContact(r, c)
	stored, _ := h.Contacts.Get(r.Context(), c.ID)
	resp := map[string]any{"contact": wireFromContact(stored)}
	if verifyErr != "" {
		resp["verify_error"] = verifyErr
	}
	writeJSON(w, http.StatusCreated, resp)
}

// DeleteAPIContact is DELETE /api/v1/contacts/{contactID}.
func (h *handlers) DeleteAPIContact(w http.ResponseWriter, r *http.Request) {
	if !h.contactsOr503(w) {
		return
	}
	if err := h.Contacts.Delete(r.Context(), r.PathValue("contactID")); err != nil {
		if errors.Is(err, contact.ErrNotFound) {
			writeError(w, http.StatusNotFound, "contact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostAPIContactVerify is POST /api/v1/contacts/{contactID}/verify.
func (h *handlers) PostAPIContactVerify(w http.ResponseWriter, r *http.Request) {
	if !h.contactsOr503(w) {
		return
	}
	c, err := h.Contacts.Get(r.Context(), r.PathValue("contactID"))
	if err != nil {
		if errors.Is(err, contact.ErrNotFound) {
			writeError(w, http.StatusNotFound, "contact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	verifyErr := h.verifyContact(r, &c)
	stored, _ := h.Contacts.Get(r.Context(), c.ID)
	resp := map[string]any{"contact": wireFromContact(stored)}
	if verifyErr != "" {
		resp["verify_error"] = verifyErr
	}
	writeJSON(w, http.StatusOK, resp)
}
