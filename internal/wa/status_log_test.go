package wa

import (
	"context"
	"errors"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// TestLogStatusPostInsertsThenMarksSentOnSuccess guards the one call site
// that writes a wa_messages row for a status/story post — StatusPoster
// bypasses the ordinary send queue entirely (capabilities.go's doc
// comment), so sender.go's worker never runs for this path and nothing
// else would ever call Insert/MarkSent for it.
func TestLogStatusPostInsertsThenMarksSentOnSuccess(t *testing.T) {
	msgs := newStubMsgs()
	mgr := newTestManager(nil, msgs)

	msg := &message.Message{ID: NewID(), SessionID: "sess-1", ToPhone: "status@broadcast",
		Body: "hello", Category: message.CategoryManual}

	if err := mgr.LogStatusPost(context.Background(), msg, "wamid-1", nil); err != nil {
		t.Fatalf("LogStatusPost: %v", err)
	}

	msgs.mu.Lock()
	defer msgs.mu.Unlock()
	if len(msgs.insertedIDs) != 1 || msgs.insertedIDs[0] != msg.ID {
		t.Fatalf("insertedIDs = %v, want exactly [%q]", msgs.insertedIDs, msg.ID)
	}
	if waID := msgs.sent[msg.ID]; waID != "wamid-1" {
		t.Fatalf("MarkSent recorded waID = %q, want %q", waID, "wamid-1")
	}
	if _, failed := msgs.failed[msg.ID]; failed {
		t.Errorf("row was marked failed, want sent")
	}
}

// TestLogStatusPostMarksFailedWhenPostingErrored guards the error path:
// the audit row still exists (so a failed status post is visible in the
// log at all), but as failed, carrying the error text, never as sent.
func TestLogStatusPostMarksFailedWhenPostingErrored(t *testing.T) {
	msgs := newStubMsgs()
	mgr := newTestManager(nil, msgs)

	msg := &message.Message{ID: NewID(), SessionID: "sess-1", ToPhone: "status@broadcast",
		Body: "hello", Category: message.CategoryManual}

	postErr := errors.New("waxum: client not connected")
	if err := mgr.LogStatusPost(context.Background(), msg, "", postErr); err != nil {
		t.Fatalf("LogStatusPost: %v", err)
	}

	msgs.mu.Lock()
	defer msgs.mu.Unlock()
	if len(msgs.insertedIDs) != 1 {
		t.Fatalf("insertedIDs = %v, want exactly one row", msgs.insertedIDs)
	}
	if got := msgs.failed[msg.ID]; got != postErr.Error() {
		t.Errorf("failed[%q] = %q, want %q", msg.ID, got, postErr.Error())
	}
	if _, sent := msgs.sent[msg.ID]; sent {
		t.Errorf("row was marked sent, want failed")
	}
}

// TestLogStatusPostWithNoMessageRepositoryReturnsTheSentinel mirrors
// Dispatch's own ErrNoMessageRepository guard — a Manager built with a nil
// message repository must fail cleanly here too, not panic on m.msgs.Insert.
func TestLogStatusPostWithNoMessageRepositoryReturnsTheSentinel(t *testing.T) {
	mgr := newTestManager(nil, nil)
	msg := &message.Message{ID: NewID(), SessionID: "sess-1", ToPhone: "status@broadcast", Body: "hi"}

	if err := mgr.LogStatusPost(context.Background(), msg, "wamid-1", nil); !errors.Is(err, ErrNoMessageRepository) {
		t.Errorf("err = %v, want ErrNoMessageRepository", err)
	}
}
