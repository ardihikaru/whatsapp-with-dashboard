package handler

import (
	"net/http"
	"strconv"
	"time"
)

// apiInbound is the wire shape for GET /api/v1/inbound. Field names and the
// bare-array response are pinned by a deployed consumer
// (pkg/wsmclient/inbound.go in laundry-saas-v1) — see the brief at
// docs/inbound-api.md. Do not rename, nest or otherwise reshape this without
// updating that client first.
type apiInbound struct {
	ID         string    `json:"id"`
	FromPhone  string    `json:"from_phone"`
	Body       string    `json:"body"`
	ReceivedAt time.Time `json:"received_at"`
	ClientRef  string    `json:"client_ref"`
	TenantRef  string    `json:"tenant_ref"`
}

// GetAPIInbound is GET /api/v1/inbound — a fleet-level READ of customer
// replies (wa_inbound_messages), for a consumer polling for answers to
// questions it asked over WhatsApp (see docs/brief-inbound-api.md).
//
// This is a read endpoint, not a webhook, on purpose: this gateway is
// shared across several products and environments, and pushing would make
// it a fan-out router holding a URL, a secret, a retry policy and a DLQ per
// subscriber — a queue it explicitly is not. A read endpoint adds no such
// state: the consumer decides its own poll cadence and there is nothing
// here to register, revoke or replay.
//
// Results are always OLDEST FIRST (Repository.Since's contract). That is
// behavioural, not cosmetic: a consumer resolving a reply against the open
// question it answers must not let a later message close a question an
// earlier one already answered — replaying newest-first or unordered risks
// exactly that.
//
// client_ref/tenant_ref are the tier stamp described in the brief: copied
// from the most recent outbound message to the same phone within a bounded
// window, and empty when no candidate existed. One gateway serves several
// products and environments, so a consumer MUST treat a row whose
// client_ref it cannot resolve locally as not its own and ignore it —
// empty (or someone else's) is the safe outcome, never a guess.
func (h *handlers) GetAPIInbound(w http.ResponseWriter, r *http.Request) {
	if h.Inbound == nil {
		// Bare writeError, not writeBackendBusy: writeBackendBusy sets
		// Retry-After, which would advertise a transient condition when the
		// real meaning is "this gateway has no inbound support configured"
		// — a caller should not retry that. This also matches how
		// h.Calls/h.Objects are nil-guarded elsewhere (calls_ui.go,
		// api_media.go) and keeps every pre-existing test server, which
		// does not set Deps.Inbound, answering 503 instead of panicking.
		writeError(w, http.StatusServiceUnavailable, "inbound log unavailable")
		return
	}

	// since is required, not defaulted, so a caller that forgot it fails
	// loudly (400) instead of silently reading the entire table.
	raw := r.URL.Query().Get("since")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "since is required (RFC3339)")
		return
	}
	since, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "since must be RFC3339")
		return
	}

	// limit: a garbage value becomes 0 and the repository clamps it — do
	// not re-clamp here, the house convention (see message/repository.go)
	// keeps clamping in the repo, not the handler.
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	msgs, err := h.Inbound.Since(r.Context(), since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read inbound messages")
		return
	}

	out := make([]apiInbound, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, apiInbound{
			ID:         m.ID,
			FromPhone:  m.FromPhone,
			Body:       m.Body,
			ReceivedAt: m.ReceivedAt,
			ClientRef:  m.ClientRef,
			TenantRef:  m.TenantRef,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
