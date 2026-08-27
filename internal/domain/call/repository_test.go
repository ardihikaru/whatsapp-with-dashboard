package call

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// newTestRepo mirrors internal/domain/message's helper of the same name:
// a fresh SQLite pool, schema created via EnsureSchema, two seeded sessions.
func newTestRepo(t *testing.T) (*SQLRepository, context.Context) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sess-1", "sess-2"} {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
			 VALUES (?, ?, '628000', '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z')`,
			s, s); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepository(pool), ctx
}

var idSeq int

func (r *SQLRepository) seed(t *testing.T, ctx context.Context, c Call) Call {
	t.Helper()
	idSeq++
	c.ID = fmt.Sprintf("call-%04d", idSeq)
	if c.SessionID == "" {
		c.SessionID = "sess-1"
	}
	if c.ToPhone == "" {
		c.ToPhone = "628123456789"
	}
	if c.Kind == "" {
		c.Kind = KindTTS
	}
	if err := r.Insert(ctx, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCallInsertAndRecentRoundTrip proves a placed call's fields survive
// Insert and come back through Recent, defaulting to StatusPlacing.
func TestCallInsertAndRecentRoundTrip(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Call{
		Kind:        KindTTS,
		Text:        "your OTP is 1234",
		Voice:       "en-US-female",
		APIClientID: "api-1",
	})

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent returned %d rows, want 1", len(got))
	}
	c := got[0]
	if c.Status != StatusPlacing {
		t.Errorf("Status = %q, want %q", c.Status, StatusPlacing)
	}
	if c.Kind != KindTTS || c.Text != "your OTP is 1234" || c.Voice != "en-US-female" {
		t.Errorf("call = %+v, unexpected tts fields", c)
	}
	if c.APIClientID != "api-1" {
		t.Errorf("APIClientID = %q, want %q", c.APIClientID, "api-1")
	}

	if err := r.MarkPlaced(ctx, c.ID, "wacall-123", "https://recording.example/x.ogg"); err != nil {
		t.Fatal(err)
	}
	got, err = r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	placed := got[0]
	if placed.Status != StatusPlaced || placed.CallID != "wacall-123" ||
		placed.RecordingURL != "https://recording.example/x.ogg" {
		t.Errorf("after MarkPlaced: %+v", placed)
	}
}

// TestMarkCallFailedRecordsTheError proves MarkFailed both moves the status
// to failed and persists the error message for the operator to read.
func TestMarkCallFailedRecordsTheError(t *testing.T) {
	r, ctx := newTestRepo(t)
	c := r.seed(t, ctx, Call{Kind: KindPlay, AudioURL: "https://audio.example/a.mp3"})

	if err := r.MarkFailed(ctx, c.ID, "waxum: 422 invalid audio_url"); err != nil {
		t.Fatal(err)
	}

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent returned %d rows, want 1", len(got))
	}
	if got[0].Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got[0].Status, StatusFailed)
	}
	if got[0].Error != "waxum: 422 invalid audio_url" {
		t.Errorf("Error = %q, want the recorded message", got[0].Error)
	}
}

// TestMarkCallTerminatedSetsEndedAt proves MarkTerminated stamps EndedAt,
// the field TestCallInsertAndRecentRoundTrip never exercises.
func TestMarkCallTerminatedSetsEndedAt(t *testing.T) {
	r, ctx := newTestRepo(t)
	c := r.seed(t, ctx, Call{})

	before := time.Now().UTC()
	if err := r.MarkTerminated(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	row := got[0]
	if row.Status != StatusTerminated {
		t.Errorf("Status = %q, want %q", row.Status, StatusTerminated)
	}
	if row.EndedAt == nil {
		t.Fatal("EndedAt is nil, want a timestamp")
	}
	if row.EndedAt.Before(before.Add(-2 * time.Second)) {
		t.Errorf("EndedAt = %v, want at/after %v", row.EndedAt, before)
	}
}

// TestMarkTerminatedByCallIDFindsTheRowByWaxumsCallIDNotOurRowID proves the
// Phase 6b §0 gap-fix: a row placed and marked placed under waxum's own
// call_id can be terminated by that call_id alone (the handler never has
// our row id in hand at that point), and a call_id scoped to a DIFFERENT
// session is not touched — one session must not be able to terminate
// another session's row by guessing/reusing a call_id.
func TestMarkTerminatedByCallIDFindsTheRowByWaxumsCallIDNotOurRowID(t *testing.T) {
	r, ctx := newTestRepo(t)
	c := r.seed(t, ctx, Call{SessionID: "sess-1"})
	if err := r.MarkPlaced(ctx, c.ID, "wacall-terminate-me", ""); err != nil {
		t.Fatal(err)
	}
	// A same-call_id row on a DIFFERENT session must be untouched.
	other := r.seed(t, ctx, Call{SessionID: "sess-2"})
	if err := r.MarkPlaced(ctx, other.ID, "wacall-terminate-me", ""); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	if err := r.MarkTerminatedByCallID(ctx, "sess-1", "wacall-terminate-me"); err != nil {
		t.Fatal(err)
	}

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent(sess-1) returned %d rows, want 1", len(got))
	}
	row := got[0]
	if row.Status != StatusTerminated {
		t.Errorf("sess-1 row Status = %q, want %q", row.Status, StatusTerminated)
	}
	if row.EndedAt == nil {
		t.Fatal("sess-1 row EndedAt is nil, want a timestamp")
	}
	if row.EndedAt.Before(before.Add(-2 * time.Second)) {
		t.Errorf("EndedAt = %v, want at/after %v", row.EndedAt, before)
	}

	gotOther, err := r.Recent(ctx, "sess-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotOther) != 1 {
		t.Fatalf("Recent(sess-2) returned %d rows, want 1", len(gotOther))
	}
	if gotOther[0].Status != StatusPlaced {
		t.Errorf("sess-2 row Status = %q, want %q (must be untouched by sess-1's terminate)", gotOther[0].Status, StatusPlaced)
	}
	if gotOther[0].EndedAt != nil {
		t.Error("sess-2 row EndedAt is set, want nil (must be untouched by sess-1's terminate)")
	}
}

// TestDeletingASessionCascadesToItsCalls proves the FK from migrations/0004
// carries an ON DELETE CASCADE, mirroring wa_messages' own FK behaviour —
// deleting a session must not orphan its call rows.
func TestDeletingASessionCascadesToItsCalls(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Call{SessionID: "sess-2"})

	got, err := r.Recent(ctx, "sess-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent before delete returned %d rows, want 1", len(got))
	}

	if _, err := r.pool.ExecContext(ctx, `DELETE FROM wa_sessions WHERE id = 'sess-2'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	got, err = r.Recent(ctx, "sess-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Recent after deleting the session returned %d rows, want 0 (FK cascade did not fire)", len(got))
	}
}
