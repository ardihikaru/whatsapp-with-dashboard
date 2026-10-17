// contacts_ui.go: the owner-managed WhatsApp contact book (internal/domain/
// contact) — a GLOBAL (not per-session) name/phone/aliases CRUD, each
// number verifiable on WhatsApp through the existing checkContacts
// (contacts_check.go) with an auto-routed session, the same routing
// selectSendSession/selectForSend already use. Follows known_groups_ui.go's
// shape, but FIXES the bug that file carries: known_group_rows (table
// <tr>s) renders into #known-group-form-error on a successful create — the
// error div ends up holding row markup. Here the create form's hx-target
// is the error div AND ONLY the error div; the rows table is refreshed
// separately via an out-of-band swap (contact_rows_oob), the same
// hx-swap-oob="true" technique partials/log_rows.html's log_more already
// uses in this codebase.
package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// contactsPageData drives GET /contacts.
type contactsPageData struct {
	PageBase
}

// GetContacts renders the /contacts CRUD page. Never gated by any feature
// flag, same as GetKnownGroups.
func (h *handlers) GetContacts(w http.ResponseWriter, r *http.Request) {
	data := contactsPageData{}
	data.PageBase = h.newPageBase(r, "Contacts", "/contacts")
	h.Renderer.RenderPage(w, "contacts", data)
}

// contactRowsData feeds the contact_rows / contact_rows_oob partials.
type contactRowsData struct {
	Error    string
	Contacts []contact.Contact
}

// contacts lists every saved contact, newest first. h.Contacts == nil
// renders an empty list, the same nil-guard knownGroups (known_groups_ui.go)
// uses.
func (h *handlers) contacts(r *http.Request) []contact.Contact {
	if h.Contacts == nil {
		return nil
	}
	cs, err := h.Contacts.List(r.Context())
	if err != nil {
		return nil
	}
	return cs
}

// GetContactRows is the list page's poll target — a direct (non-OOB) swap.
func (h *handlers) GetContactRows(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Contacts: h.contacts(r)})
}

// contactCreateResponseData feeds contact_create_response: an error (or
// nothing) swapped into the create form's own target, plus the rows table
// refreshed out-of-band regardless of outcome.
type contactCreateResponseData struct {
	Error string
	Rows  contactRowsData
}

func (h *handlers) renderContactCreateError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "contact_create_response", contactCreateResponseData{
		Error: errMsg,
		Rows:  contactRowsData{Contacts: h.contacts(r)},
	})
}

// splitAliasesInput turns the aliases textfield's raw value into a
// normalized (lowercased, trimmed, deduplicated-by-nothing-fancy) comma
// string, the storage shape migrations/0012_contacts.sql documents.
// Splitting on both commas and newlines mirrors splitPhonesInput's own
// leniency (verify_ui.go) — an operator pasting a multi-line export must
// not have to reformat it first.
func splitAliasesInput(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// pickVerifySnapshot chooses the connected session used for ONE owner-
// contact usync lookup (checkContacts), and is deliberately WIDER than
// send routing: verification is a single read-only CheckContacts call
// against whichever device is live, never a message a recipient sees, so
// unlike selectSendSession/SelectForRoute(wa.Route{}) it has no reason to
// stay confined to the ungrouped, non-master pool a send must respect
// (CLAUDE.md's load-balancing rules exist to keep sends away from a
// single-point-of-failure master or a group's dedicated numbers — neither
// concern applies to reading usync results).
//
// Root cause this exists to fix (observed live 2026-10-17): on a host
// where every connected session is either is_master or carries a
// non-empty GroupCode, wa.Route{} admits none of them
// (wa.EligibleForRoute), so verification answered "no connected session
// available" with 3 sessions connected. Tiers, in order:
//
//  1. wa.SelectSnapshotForRoute(snaps, wa.Route{}) — the ordinary
//     ungrouped/non-master pool, UNCHANGED preference from before this fix;
//  2. wa.SelectSnapshotForRoute(snaps, wa.Route{AllowMaster: true}) — same
//     ungrouped pool, now also admitting a master;
//  3. any connected snapshot at all, regardless of GroupCode or IsMaster —
//     picked deterministically: non-master before master (an owner-contact
//     lookup has no reason to prefer burning the master's socket when an
//     ordinary grouped session is just as able to answer it), then the
//     stable tie-break every other ranking in this package uses, lowest
//     ID.
//
// Each tier still requires Connected == true; a Route{} with GroupCode ""
// never matches a non-empty GroupCode, which is why tier 3 must fall back
// to a hand-rolled scan rather than another Route value — there is no
// Route that means "any group".
func pickVerifySnapshot(snaps []wa.Snapshot) (wa.Snapshot, bool) {
	if s, ok := wa.SelectSnapshotForRoute(snaps, wa.Route{}); ok {
		return s, true
	}
	if s, ok := wa.SelectSnapshotForRoute(snaps, wa.Route{AllowMaster: true}); ok {
		return s, true
	}
	var best wa.Snapshot
	found := false
	for _, s := range snaps {
		if !s.Connected {
			continue
		}
		if !found {
			best, found = s, true
			continue
		}
		if better := func() bool {
			if s.IsMaster != best.IsMaster {
				return !s.IsMaster // non-master beats master
			}
			return s.ID < best.ID
		}(); better {
			best = s
		}
	}
	return best, found
}

// verifyContact checks one contact's phone through a connected session
// chosen by pickVerifySnapshot (wider than send routing — see that
// function's doc comment for why) and persists the outcome via
// SetVerification. It NEVER writes StatusNotRegistered on a
// routing/backend failure — only a real "not_registered" verdict from
// checkContacts does that — matching contactVerdict's own doc comment
// (contacts_check.go) on why "unknown" and "not_registered" must stay
// distinct. Returns a human-readable error to surface to the operator, or
// "" on a clean verify (any resulting status, including "unknown" for a
// verdict waxum itself did not answer).
func (h *handlers) verifyContact(r *http.Request, c *contact.Contact) string {
	s, ok := pickVerifySnapshot(h.Manager.Snapshots())
	if !ok {
		h.setContactUnknown(r, c.ID)
		return "could not verify: no connected session available"
	}
	verdicts, err := h.checkContacts(r.Context(), s.ID, []string{c.Phone})
	if err != nil {
		h.setContactUnknown(r, c.ID)
		return "could not verify: " + verifyErrorMessage(err)
	}
	if len(verdicts) == 0 {
		h.setContactUnknown(r, c.ID)
		return "could not verify: no result returned"
	}
	v := verdicts[0]
	status, jid, errMsg := contactVerificationOutcome(v)
	_ = h.Contacts.SetVerification(r.Context(), c.ID, jid, status, time.Time{})
	return errMsg
}

// contactVerificationOutcome is the pure mapping verifyContact applies to
// one checkContacts verdict — split out so it is unit-testable with
// literal contactVerdict values, without a live connected session (package
// handler's fakeGatewayBackend cannot reach wa.StateConnected at all; see
// send_test.go's doc comment on the same constraint). It must NEVER map
// anything other than a genuine "not_registered" verdict to
// contact.StatusNotRegistered — contactVerdict's own doc comment
// (contacts_check.go) is explicit that "unknown" and "invalid" are not the
// same claim as "confirmed absent from WhatsApp".
func contactVerificationOutcome(v contactVerdict) (status contact.Status, jid string, errMsg string) {
	switch v.Status {
	case contactStatusRegistered:
		return contact.StatusRegistered, v.JID, ""
	case contactStatusNotRegistered:
		return contact.StatusNotRegistered, "", ""
	case contactStatusInvalid:
		return contact.StatusUnknown, "", "could not verify: " + v.Error
	default: // contactStatusUnknown
		return contact.StatusUnknown, "", "could not verify: waxum returned no verdict for this number"
	}
}

func (h *handlers) setContactUnknown(r *http.Request, id string) {
	_ = h.Contacts.SetVerification(r.Context(), id, "", contact.StatusUnknown, time.Time{})
}

// PostContactCreate is POST /contacts — creates a contact from an
// operator-supplied name/phone/aliases, then immediately verifies it
// (still succeeding the create on a verify failure; see verifyContact's
// doc comment).
func (h *handlers) PostContactCreate(w http.ResponseWriter, r *http.Request) {
	if h.Contacts == nil {
		h.renderContactCreateError(w, r, "contacts are not available on this deployment")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderContactCreateError(w, r, "invalid form submission")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	rawPhone := strings.TrimSpace(r.FormValue("phone"))
	if name == "" {
		h.renderContactCreateError(w, r, "name is required")
		return
	}
	digits, err := wa.NormalizePhone(rawPhone)
	if err != nil {
		h.renderContactCreateError(w, r, err.Error())
		return
	}
	aliases := strings.Join(splitAliasesInput(r.FormValue("aliases")), ",")

	c := &contact.Contact{ID: wa.NewID(), Name: name, Phone: digits, Aliases: aliases}
	if err := h.Contacts.Insert(r.Context(), c); err != nil {
		msg := err.Error()
		if errors.Is(err, contact.ErrDuplicate) {
			msg = "a contact with this phone number already exists"
		}
		h.renderContactCreateError(w, r, msg)
		return
	}

	verifyErr := h.verifyContact(r, c)
	h.renderContactCreateError(w, r, verifyErr)
}

// PostContactDelete is POST /contacts/{id}/delete.
func (h *handlers) PostContactDelete(w http.ResponseWriter, r *http.Request) {
	if h.Contacts == nil {
		h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Error: "contacts are not available on this deployment", Contacts: h.contacts(r)})
		return
	}
	if err := h.Contacts.Delete(r.Context(), r.PathValue("id")); err != nil {
		msg := err.Error()
		if errors.Is(err, contact.ErrNotFound) {
			msg = "contact not found"
		}
		h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Error: msg, Contacts: h.contacts(r)})
		return
	}
	h.GetContactRows(w, r)
}

// PostContactVerify is POST /contacts/{id}/verify — re-runs verifyContact
// for one existing contact and re-renders the rows table directly (this
// button has no separate error target, unlike the create form).
func (h *handlers) PostContactVerify(w http.ResponseWriter, r *http.Request) {
	if h.Contacts == nil {
		h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Error: "contacts are not available on this deployment", Contacts: h.contacts(r)})
		return
	}
	c, err := h.Contacts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		if errors.Is(err, contact.ErrNotFound) {
			msg = "contact not found"
		}
		h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Error: msg, Contacts: h.contacts(r)})
		return
	}
	errMsg := h.verifyContact(r, &c)
	h.Renderer.RenderPartial(w, "contact_rows", contactRowsData{Error: errMsg, Contacts: h.contacts(r)})
}
