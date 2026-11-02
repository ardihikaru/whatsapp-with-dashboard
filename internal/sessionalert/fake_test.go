package sessionalert

import (
	"context"
	"errors"
	"sync"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
)

// fakeGateway is a minimal, in-memory Gateway double.
type fakeGateway struct {
	mu      sync.Mutex
	snaps   []Snapshot
	sends   []fakeSend
	sendErr error

	// members maps sessionID -> the set of group JIDs that session
	// belongs to, for Member's fallback-tier membership check. A session
	// id with no entry at all answers "unknown" (an error), the fail-
	// closed default — see memberErrFor for forcing a specific error.
	members     map[string]map[string]bool
	memberErrs  map[string]error
	memberCalls []string // session ids Member was actually asked about, in order

	events []string // level+note pairs recorded via RecordEvent, "level: note"
}

type fakeSend struct {
	SessionID, GroupJID, Text string
}

func newFakeGateway(snaps ...Snapshot) *fakeGateway {
	return &fakeGateway{snaps: snaps, members: map[string]map[string]bool{}, memberErrs: map[string]error{}}
}

func (g *fakeGateway) Snapshots() []Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Snapshot(nil), g.snaps...)
}

func (g *fakeGateway) Send(ctx context.Context, sessionID, groupJID, text string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sendErr != nil {
		return g.sendErr
	}
	g.sends = append(g.sends, fakeSend{SessionID: sessionID, GroupJID: groupJID, Text: text})
	return nil
}

// setMember marks sessionID as a (non-)member of groupJID for Member's
// lookup — call with member=true to make a session eligible for the
// fallback tier.
func (g *fakeGateway) setMember(sessionID, groupJID string, member bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.members[sessionID] == nil {
		g.members[sessionID] = map[string]bool{}
	}
	g.members[sessionID][groupJID] = member
}

// setMemberErr makes Member(sessionID, ...) fail — simulating "membership
// unknown" (e.g. a backend error fetching the joined-group list).
func (g *fakeGateway) setMemberErr(sessionID string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.memberErrs[sessionID] = err
}

func (g *fakeGateway) Member(ctx context.Context, sessionID, groupJID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.memberCalls = append(g.memberCalls, sessionID)
	if err, ok := g.memberErrs[sessionID]; ok {
		return false, err
	}
	if groups, ok := g.members[sessionID]; ok {
		return groups[groupJID], nil
	}
	return false, errors.New("fake: membership unknown for " + sessionID)
}

func (g *fakeGateway) RecordEvent(level, note string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, level+": "+note)
}

func (g *fakeGateway) sentCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sends)
}

func (g *fakeGateway) lastSend() fakeSend {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sends) == 0 {
		return fakeSend{}
	}
	return g.sends[len(g.sends)-1]
}

func (g *fakeGateway) memberCallCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.memberCalls)
}

func (g *fakeGateway) eventCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.events)
}

func (g *fakeGateway) lastEvent() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return ""
	}
	return g.events[len(g.events)-1]
}

// fakeSettings is an in-memory sa.Repository double.
type fakeSettings struct {
	mu sync.Mutex
	s  sa.Settings
}

func newFakeSettings(s sa.Settings) *fakeSettings {
	return &fakeSettings{s: s}
}

func (f *fakeSettings) Get(context.Context) (sa.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s, nil
}

func (f *fakeSettings) Save(_ context.Context, s sa.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s = s
	return nil
}

func (f *fakeSettings) ClaimDailyDate(_ context.Context, date string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.s.LastDailyDate == date {
		return false, nil
	}
	f.s.LastDailyDate = date
	return true, nil
}
