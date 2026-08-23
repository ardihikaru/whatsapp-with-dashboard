package wa

import (
	"context"
	"regexp"
	"strings"
)

var allDigits = regexp.MustCompile(`^\d+$`)

// GroupInfo is the value-copy DTO for a joined group. Like Snapshot, it exists so
// no backend-specific type escapes this package: handlers must not hold a
// whatsmeow types.GroupInfo whose lifetime is tied to the client (and a REST
// backend has no such type to hold in the first place).
//
// The json tags are not decoration — this struct is marshalled straight to the API
// response. Without them Go emits "JID"/"Name"/"Participants" while openapi.yaml
// promises "jid"/"name"/"participants", and every client written from the spec
// silently reads nulls. Caught by curling the live endpoint, not by any test.
type GroupInfo struct {
	JID          string `json:"jid"`
	Name         string `json:"name"`
	Participants int    `json:"participants"`
}

// ParseGroupJID accepts "120363xxxxxxxxxxxx@g.us" or the bare id and returns a
// group JID. It is the group-side twin of NormalizePhone: ONE validator, so the
// alerter, the API and any future picker reject the same inputs identically.
//
// The Server assertion is the whole point of this function. A user JID
// (…@s.whatsapp.net) passed to the group endpoint would otherwise send the alert
// to one person's private chat and report success — the alert is "delivered", the
// on-call group never sees it, and nobody finds out until an incident is missed.
func ParseGroupJID(raw string) (JID, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return EmptyJID, ErrNotAGroupJID
	}
	// An operator pasting from GET /sessions/{id}/groups has the full JID; one
	// reading the id off a screen may not. Accept both, canonicalise to one.
	if allDigits.MatchString(s) {
		return JID{User: s, Server: groupServer}, nil
	}
	jid, err := ParseJID(s)
	if err != nil {
		return EmptyJID, ErrNotAGroupJID
	}
	// Both halves must be checked. ParseJID("@g.us") succeeds and yields a
	// JID whose Server IS groupServer with an EMPTY User — so a Server-only
	// assertion passes it straight through to Enqueue. Caught by group_test.go's
	// "only a server" case, which was red when the check was Server alone.
	if jid.Server != groupServer || jid.User == "" {
		return EmptyJID, ErrNotAGroupJID
	}
	return jid, nil
}

// JoinedGroups asks the live backend which groups the device belongs to. This
// is how WA_ALERT_GROUP_JID is discovered: the JID is not guessable and is
// not shown anywhere in the WhatsApp UI.
//
// The connected/client checks live in the backend (backend_meow.go) — the
// rule the rest of this package follows: never hold s.mu across a network
// round trip. Holding it here too would let one slow group fetch block
// Snapshot, and therefore the whole sessions page, for as long as WhatsApp
// felt like taking.
func (s *ManagedSession) JoinedGroups(ctx context.Context) ([]GroupInfo, error) {
	return s.mgr.backend.JoinedGroups(ctx, s)
}
