package wa

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestKeepAliveTimeoutRestoredStateTransition(t *testing.T) {
	repo := newStubSessions()
	s := testSession(newTestManager(repo, newStubMsgs()), StateConnected)

	s.handleEvent(&events.KeepAliveTimeout{ErrorCount: 1, LastSuccess: time.Now().Add(-45 * time.Second)})
	if got := s.Snapshot().State; got != StateDisconnected {
		t.Fatalf("after timeout state = %s, want disconnected", got)
	}
	if got := repo.lastStatus(s.ID); got != string(StateDisconnected) {
		t.Fatalf("after timeout persisted = %q, want disconnected", got)
	}
	assertRingContains(t, s, "keepalive timeout #1")

	s.handleEvent(&events.KeepAliveRestored{})
	if got := s.Snapshot().State; got != StateConnected {
		t.Fatalf("after restored state = %s, want connected", got)
	}
	if got := repo.lastStatus(s.ID); got != string(StateConnected) {
		t.Fatalf("after restored persisted = %q, want connected", got)
	}
}

func TestKeepAliveRestoredDoesNotReviveTerminalState(t *testing.T) {
	repo := newStubSessions()
	s := testSession(newTestManager(repo, newStubMsgs()), StateLoggedOut)

	s.handleEvent(&events.KeepAliveRestored{})

	if got := s.Snapshot().State; got != StateLoggedOut {
		t.Fatalf("state = %s, want logged_out", got)
	}
	if got := repo.lastStatus(s.ID); got != "" {
		t.Fatalf("persisted = %q, want no transition", got)
	}
}

func TestDisconnectedStateTransition(t *testing.T) {
	repo := newStubSessions()
	s := testSession(newTestManager(repo, newStubMsgs()), StateConnected)

	s.handleEvent(&events.Disconnected{})

	if got := s.Snapshot().State; got != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", got)
	}
	if got := repo.lastStatus(s.ID); got != string(StateDisconnected) {
		t.Fatalf("persisted = %q, want disconnected", got)
	}
}

func TestUndecryptableStatusBroadcastIsOnlyTypedRingEvent(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateConnected)

	s.handleEvent(&events.UndecryptableMessage{Info: types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:   types.StatusBroadcastJID,
			Sender: types.NewJID("62603544014938", types.HiddenUserServer),
		},
	}})

	notes := ringNotes(s)
	if len(notes) != 1 {
		t.Fatalf("ring notes = %v, want only typed event", notes)
	}
	if !strings.Contains(notes[0], "*events.UndecryptableMessage") {
		t.Fatalf("ring note = %q, want typed event", notes[0])
	}
}

func TestUndecryptableNonStatusMessageAddsDiagnosticRingEvent(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateConnected)

	s.handleEvent(&events.UndecryptableMessage{Info: types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID("120363000000000000", types.GroupServer),
			Sender: types.NewJID("6281200000000", types.DefaultUserServer),
		},
	}})

	assertRingContains(t, s, "undecryptable message from")
}

func assertRingContains(t *testing.T, s *ManagedSession, want string) {
	t.Helper()
	for _, note := range ringNotes(s) {
		if strings.Contains(note, want) {
			return
		}
	}
	t.Fatalf("ring missing %q; notes = %v", want, ringNotes(s))
}

func ringNotes(s *ManagedSession) []string {
	events := s.events.Snapshot()
	notes := make([]string, 0, len(events))
	for _, evt := range events {
		notes = append(notes, evt.Note)
	}
	return notes
}
