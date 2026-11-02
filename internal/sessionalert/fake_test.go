package sessionalert

import (
	"context"
	"sync"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
)

// fakeGateway is a minimal, in-memory Gateway double.
type fakeGateway struct {
	mu      sync.Mutex
	snaps   []Snapshot
	sends   []fakeSend
	sendErr error
}

type fakeSend struct {
	SessionID, GroupJID, Text string
}

func newFakeGateway(snaps ...Snapshot) *fakeGateway {
	return &fakeGateway{snaps: snaps}
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
