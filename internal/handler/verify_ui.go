package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// verifyPageData drives GET /verify. Connected mirrors sendPageData/
// callsPageData's own field exactly (send.go, calls_ui.go) — the same
// "Automatic routing" rule those pages use via selectSendSession applies
// here too. Supported is resolved once here, rather than in the template,
// so a whatsmeow-backed deployment (which has no wa.ContactDirectory at
// all — see capabilities.go) renders a plain explanatory card instead of a
// form that could only ever answer wa.ErrUnsupported.
type verifyPageData struct {
	PageBase
	Connected []wa.Snapshot
	Supported bool
}

// verifyRowsData feeds the verify_rows partial — the SAME partial every
// PostVerify outcome renders, success or failure, per PostSend's (send.go)
// documented rule that htmx only swaps a 2xx response: a validation
// failure or a backend error travels in Error, never as a 4xx status.
// Unlike callRowsData (calls_ui.go) this has no persisted history to
// re-fetch on every render — there is no wa_contacts table — so Verdicts
// is exactly and only what the triggering POST produced.
type verifyRowsData struct {
	Error    string
	Verdicts []contactVerdict
}

// connectedSnapshots filters a fleet snapshot down to the sessions the
// picker may route to. Factored out of GetVerify as its own function
// specifically so it has a direct unit test
// (TestConnectedSnapshotsExcludesDisconnectedSessions,
// verify_ui_test.go): wa.Manager is a concrete struct with no seam this
// package can use to synthesize a genuinely wa.Snapshot.Connected==true
// session (Connected is computed from unexported ManagedSession state plus
// a live Backend.Live poll — session.go's Snapshot doc comment), so the
// only way to pin "only connected sessions reach the picker" with real
// red/green evidence is to test the filter itself against literal
// wa.Snapshot values, the same technique internal/wa/load_balance_test.go
// already uses for SelectConnected's own literal-Snapshot fixtures.
func connectedSnapshots(snaps []wa.Snapshot) []wa.Snapshot {
	var out []wa.Snapshot
	for _, s := range snaps {
		if s.Connected {
			out = append(out, s)
		}
	}
	return out
}

// GetVerify renders the /verify page.
func (h *handlers) GetVerify(w http.ResponseWriter, r *http.Request) {
	connected := connectedSnapshots(h.Manager.Snapshots())
	_, err := h.Manager.ContactDirectory()
	data := verifyPageData{
		Connected: connected,
		Supported: !errors.Is(err, wa.ErrUnsupported),
	}
	data.PageBase = h.newPageBase(r, "Verify Number", "/verify")
	h.Renderer.RenderPage(w, "verify", data)
}

// splitPhonesInput turns the phones textarea's raw value into individual
// candidate numbers, splitting on BOTH newlines and commas — an operator
// pasting a comma-separated export must not have to reformat it into one
// number per line first. Blank entries (including a trailing textarea
// newline) are dropped here, before checkContacts ever sees them: PostVerify
// needs the count of REAL candidates to tell "you typed nothing" apart from
// "you typed something and it failed to parse", and checkContacts itself
// treats a caller with zero rows as a legitimate (if useless) empty result,
// not an error — see that function's doc comment.
func splitPhonesInput(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// verifyErrorMessage maps a checkContacts error to operator-facing copy.
// errTooManyPhones's own message is already readable and is passed through
// verbatim (the default case); wa.ErrUnsupported/wa.ErrNotConnected get
// friendlier wording than their sentinel text, mirroring
// PostCallsTTS/PostCallsPlay's inline error handling (calls_ui.go) one
// layer down.
func verifyErrorMessage(err error) string {
	switch {
	case errors.Is(err, wa.ErrUnsupported):
		return "number verification is not supported by this backend"
	case errors.Is(err, wa.ErrNotConnected):
		return "session not connected"
	default:
		return err.Error()
	}
}

// verifyRows is PostVerify's single render point for the verify_rows
// partial — callRowsWithError's shape (calls_ui.go) minus the poll: there
// is no persisted verification history to re-fetch, so this is called only
// from PostVerify itself.
func (h *handlers) verifyRows(w http.ResponseWriter, data verifyRowsData) {
	h.Renderer.RenderPartial(w, "verify_rows", data)
}

// PostVerify checks a batch of numbers against the active backend's
// contact directory. EVERY outcome — a validation failure, a backend
// error, or a real result set — renders verify_rows at HTTP 200: htmx only
// swaps a 2xx response (send.go's PostSend documents the same rule), so a
// 4xx here would leave the results table showing stale content with no
// explanation of why nothing changed.
func (h *handlers) PostVerify(w http.ResponseWriter, r *http.Request) {
	inputs := splitPhonesInput(r.FormValue("phones"))
	if len(inputs) == 0 {
		h.verifyRows(w, verifyRowsData{Error: "enter at least one phone number"})
		return
	}
	s, errMsg := h.selectSendSession(r)
	if errMsg != "" {
		h.verifyRows(w, verifyRowsData{Error: errMsg})
		return
	}
	verdicts, err := h.checkContacts(r.Context(), s.ID, inputs)
	if err != nil {
		h.verifyRows(w, verifyRowsData{Error: verifyErrorMessage(err)})
		return
	}
	h.verifyRows(w, verifyRowsData{Verdicts: verdicts})
}
