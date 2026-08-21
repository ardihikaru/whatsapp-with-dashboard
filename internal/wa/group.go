package wa

import (
	"context"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

var allDigits = regexp.MustCompile(`^\d+$`)

// GroupInfo is the value-copy DTO for a joined group. Like Snapshot, it exists so
// no whatsmeow type escapes this package: handlers must not hold a types.GroupInfo
// whose lifetime is tied to the client.
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
func ParseGroupJID(raw string) (types.JID, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return types.EmptyJID, ErrNotAGroupJID
	}
	// An operator pasting from GET /sessions/{id}/groups has the full JID; one
	// reading the id off a screen may not. Accept both, canonicalise to one.
	if allDigits.MatchString(s) {
		return types.NewJID(s, types.GroupServer), nil
	}
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.EmptyJID, ErrNotAGroupJID
	}
	// Both halves must be checked. types.ParseJID("@g.us") succeeds and yields a
	// JID whose Server IS types.GroupServer with an EMPTY User — so a Server-only
	// assertion passes it straight through to Enqueue. Caught by group_test.go's
	// "only a server" case, which was red when the check was Server alone.
	if jid.Server != types.GroupServer || jid.User == "" {
		return types.EmptyJID, ErrNotAGroupJID
	}
	return jid, nil
}

// JoinedGroups asks the live client which groups the device belongs to. This is
// how WA_ALERT_GROUP_JID is discovered: the JID is not guessable and is not shown
// anywhere in the WhatsApp UI.
//
// The lock is taken to read s.client and RELEASED before the network call — the
// rule the rest of this package follows. Holding s.mu across a whatsmeow round
// trip would let one slow group fetch block Snapshot, and therefore the whole
// sessions page, for as long as WhatsApp felt like taking.
func (s *ManagedSession) JoinedGroups(ctx context.Context) ([]GroupInfo, error) {
	s.mu.Lock()
	cli := s.client
	connected := s.state == StateConnected && cli != nil
	s.mu.Unlock()
	if !connected {
		return nil, ErrNotConnected
	}

	groups, err := cli.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupInfo{
			JID:          g.JID.String(),
			Name:         g.GroupName.Name,
			Participants: len(g.Participants),
		})
	}
	return out, nil
}
