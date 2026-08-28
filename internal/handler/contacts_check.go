package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// maxVerifyPhones caps how many numbers ONE checkContacts call will send to
// the backend, counting inputs AFTER blank lines are dropped (but before
// duplicate-collapsing — a request padded with the same number 200 times is
// still 200 lines a caller typed). Bulk usync against a paired device
// identity that has no second copy anywhere (CLAUDE.md's whatsmeow/waxum
// singleton-volume entries) is a ban vector; 100 is a conservative ceiling
// for an operator pasting a real contact list, not a batch job.
const maxVerifyPhones = 100

// errTooManyPhones is returned by checkContacts when the cap above is
// exceeded, so both API callers (api_contacts.go) can map it to 400 without
// string-matching an error message.
var errTooManyPhones = errors.New("too many phone numbers: max 100 per request")

// Contact-check status vocabulary. "unknown" is deliberately distinct from
// "not_registered" — see contactVerdict's doc comment.
const (
	contactStatusRegistered    = "registered"
	contactStatusNotRegistered = "not_registered"
	contactStatusInvalid       = "invalid"
	contactStatusUnknown       = "unknown"
)

// contactVerdict is one line of checkContacts' result, in INPUT order — the
// shared shape the JSON API (api_contacts.go) and the future page both
// render, so they can never disagree about what a checked number means.
type contactVerdict struct {
	// Input is exactly what the caller sent for this line, unmodified —
	// including anything checkContacts itself rejected as unparseable.
	Input string `json:"input"`
	// Phone is the normalised/canonicalised digits this verdict is about.
	// Empty when Input could not be parsed as a phone number at all
	// (Status == "invalid").
	Phone string `json:"phone,omitempty"`
	// JID is waxum's reported WhatsApp JID for Phone, when it returned one
	// (only ever set alongside Status == "registered").
	JID string `json:"jid,omitempty"`
	// Status is one of:
	//   registered     - waxum confirmed this number has a WhatsApp account.
	//   not_registered - waxum confirmed this number has none.
	//   invalid        - Input could not be parsed as a phone number at all
	//                    (wa.NormalizePhone rejected it); Error explains why.
	//   unknown        - Input parsed fine and was sent to waxum, but no
	//                    verdict came back for it. This is NOT the same as
	//                    not_registered: waxum simply did not answer about
	//                    this number, and reporting "not on WhatsApp" for a
	//                    number nobody actually checked is exactly the kind
	//                    of confident guess CLAUDE.md's rule 2 forbids.
	Status string `json:"status"`
	// Error is set only for Status == "invalid", carrying the per-row
	// wa.NormalizePhone rejection reason.
	Error string `json:"error,omitempty"`
}

// checkContacts is the ONE place phone-number verification logic lives —
// the JSON API (api_contacts.go) and the future dashboard page both call
// this instead of each hand-rolling their own normalise/dedupe/match dance,
// so they cannot silently diverge on what "registered" means.
//
// Every input is normalised INDIVIDUALLY through wa.NormalizePhone: one
// unparseable line becomes an "invalid" row for that line ONLY — it must
// never fail the whole request, and every valid sibling still gets a real
// verdict. Blank/whitespace-only lines are dropped before normalisation
// runs at all, so a trailing textarea newline never becomes a spurious
// "invalid" row.
//
// Distinct (post-normalisation) numbers are sent to the backend in ONE
// batched CheckContacts call — never one call per number — but duplicates
// still produce one output row per input line, in input order.
//
// Results are matched back to inputs by the NORMALISED DIGITS of what
// waxum returned (ContactCheck.Phone, and its JID's user part when
// present), not by slice position: WhatsApp's usync can canonicalise the
// number it was asked about (e.g. dropping a leading 9 on some Brazilian
// mobiles — see wa.ContactCheck's doc comment), so the phone that comes
// back is not guaranteed to equal the digits that were sent. A single
// unmatched result against a single queued number is still attributed to
// it (see the "singleton query" comment below) — with only one candidate
// there is no ambiguity left to resolve; with more than one queued number
// an orphaned result is left unattributed and its row becomes "unknown"
// rather than guessed at.
//
// checkContacts itself returns (nil, err) only for a genuine failure: no
// ContactDirectory on the active backend (wa.ErrUnsupported), the backend
// call failing outright, or more than maxVerifyPhones valid numbers
// (errTooManyPhones). A per-number outcome — invalid, unknown,
// not_registered — is never reported as a function error.
func (h *handlers) checkContacts(ctx context.Context, sessionID string, inputs []string) ([]contactVerdict, error) {
	cd, err := h.Manager.ContactDirectory()
	if err != nil {
		return nil, err
	}

	type row struct {
		input  string
		digits string
		err    error
	}

	rows := make([]row, 0, len(inputs))
	queue := make([]string, 0, len(inputs))
	queued := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in) == "" {
			continue
		}
		digits, nerr := wa.NormalizePhone(in)
		rows = append(rows, row{input: in, digits: digits, err: nerr})
		if nerr == nil && !queued[digits] {
			queued[digits] = true
			queue = append(queue, digits)
		}
	}

	if len(rows) > maxVerifyPhones {
		return nil, errTooManyPhones
	}
	if len(rows) == 0 {
		return []contactVerdict{}, nil
	}

	var results []wa.ContactCheck
	if len(queue) > 0 {
		results, err = cd.CheckContacts(ctx, sessionID, queue)
		if err != nil {
			return nil, err
		}
	}

	// Index every result by the normalised digits of its OWN reported
	// phone/jid, not by anything we sent — see the function doc comment on
	// why matching must go this way.
	byDigits := make(map[string]wa.ContactCheck, len(results)*2)
	for _, res := range results {
		if d, derr := wa.NormalizePhone(res.Phone); derr == nil {
			byDigits[d] = res
		}
		if res.JID != "" {
			if jid, jerr := wa.ParseJID(res.JID); jerr == nil {
				if d, derr := wa.NormalizePhone(jid.User); derr == nil {
					byDigits[d] = res
				}
			}
		}
	}

	// A singleton query (exactly one distinct number sent, exactly one
	// result back) can still fail the digit-equality lookup above when
	// WhatsApp's usync canonicalised the number we asked about (e.g. a
	// leading 9 dropped by Brazilian mobile canonicalisation — see
	// wa.ContactCheck's doc comment). There is no ambiguity to resolve in
	// that specific shape: the one result we got back can only be about
	// the one number we asked about, so attribute it directly rather than
	// reporting "unknown" for a number waxum DID answer about, just under
	// a different digit string than we sent. This is deliberately NOT
	// generalised to a multi-number query — with more than one candidate,
	// which queued number an orphaned result belongs to is genuinely
	// unknowable from Phone/JID alone, and guessing is exactly what the
	// "unknown" status exists to avoid.
	if len(queue) == 1 && len(results) == 1 {
		if _, ok := byDigits[queue[0]]; !ok {
			byDigits[queue[0]] = results[0]
		}
	}

	out := make([]contactVerdict, 0, len(rows))
	for _, r := range rows {
		if r.err != nil {
			out = append(out, contactVerdict{
				Input:  r.input,
				Status: contactStatusInvalid,
				Error:  r.err.Error(),
			})
			continue
		}
		v := contactVerdict{Input: r.input, Phone: r.digits}
		if res, ok := byDigits[r.digits]; ok {
			v.Phone = res.Phone
			v.JID = res.JID
			if res.IsRegistered {
				v.Status = contactStatusRegistered
			} else {
				v.Status = contactStatusNotRegistered
			}
		} else {
			v.Status = contactStatusUnknown
		}
		out = append(out, v)
	}
	return out, nil
}
