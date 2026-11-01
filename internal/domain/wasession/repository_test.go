package wasession

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// newTestRepo mirrors internal/domain/call's helper of the same name: a
// fresh SQLite pool, schema created via EnsureSchema.
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
	return NewRepository(pool), ctx
}

func findByID(sessions []Session, id string) (Session, bool) {
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return Session{}, false
}

// TestAFreshlyInsertedSessionIsNotAMaster proves a plain Insert (no
// SetMaster call) leaves is_master false — the DEFAULT 0 semantics
// manager_crud.go relies on to need no new field for session creation.
func TestAFreshlyInsertedSessionIsNotAMaster(t *testing.T) {
	r, ctx := newTestRepo(t)
	s := &Session{ID: "sess-1", Name: "sess-1", Phone: "628000", LastStatus: "new"}
	if err := r.Insert(ctx, s); err != nil {
		t.Fatal(err)
	}

	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after Insert")
	}
	if got.IsMaster {
		t.Error("IsMaster = true, want false for a freshly inserted session")
	}
}

// TestSetMasterRoundTripsThroughTheSessionsTable proves Insert → SetMaster →
// All carries the flag correctly in both directions (set and unset).
func TestSetMasterRoundTripsThroughTheSessionsTable(t *testing.T) {
	r, ctx := newTestRepo(t)
	s := &Session{ID: "sess-1", Name: "sess-1", Phone: "628000", LastStatus: "new"}
	if err := r.Insert(ctx, s); err != nil {
		t.Fatal(err)
	}

	if err := r.SetMaster(ctx, "sess-1", true); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after SetMaster(true)")
	}
	if !got.IsMaster {
		t.Error("IsMaster = false after SetMaster(true), want true")
	}

	if err := r.SetMaster(ctx, "sess-1", false); err != nil {
		t.Fatal(err)
	}
	all, err = r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok = findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after SetMaster(false)")
	}
	if got.IsMaster {
		t.Error("IsMaster = true after SetMaster(false), want false")
	}
}

// TestAFreshlyInsertedSessionIsUngrouped is TestAFreshlyInsertedSessionIsNotAMaster's
// twin for group_code: a plain Insert (no SetGroupCode call) leaves
// group_code "" — the DEFAULT "" semantics migrations/0009_session_group_code.sql
// relies on so every existing row keeps routing exactly as before.
func TestAFreshlyInsertedSessionIsUngrouped(t *testing.T) {
	r, ctx := newTestRepo(t)
	s := &Session{ID: "sess-1", Name: "sess-1", Phone: "628000", LastStatus: "new"}
	if err := r.Insert(ctx, s); err != nil {
		t.Fatal(err)
	}

	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after Insert")
	}
	if got.GroupCode != "" {
		t.Errorf("GroupCode = %q, want \"\" for a freshly inserted session", got.GroupCode)
	}
}

// TestSetGroupCodeRoundTripsThroughTheSessionsTable is
// TestSetMasterRoundTripsThroughTheSessionsTable's twin for group_code:
// Insert → SetGroupCode → All carries the code correctly, and clearing it
// back to "" (not merely setting it once) round-trips too.
func TestSetGroupCodeRoundTripsThroughTheSessionsTable(t *testing.T) {
	r, ctx := newTestRepo(t)
	s := &Session{ID: "sess-1", Name: "sess-1", Phone: "628000", LastStatus: "new"}
	if err := r.Insert(ctx, s); err != nil {
		t.Fatal(err)
	}

	if err := r.SetGroupCode(ctx, "sess-1", "alpha"); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after SetGroupCode(alpha)")
	}
	if got.GroupCode != "alpha" {
		t.Errorf("GroupCode = %q after SetGroupCode(alpha), want %q", got.GroupCode, "alpha")
	}

	if err := r.SetGroupCode(ctx, "sess-1", ""); err != nil {
		t.Fatal(err)
	}
	all, err = r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok = findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after SetGroupCode(\"\")")
	}
	if got.GroupCode != "" {
		t.Errorf("GroupCode = %q after SetGroupCode(\"\"), want \"\"", got.GroupCode)
	}
}

// TestSetNameRoundTripsThroughTheSessionsTable proves Insert -> SetName -> All
// carries the new name, and that only the targeted row changes.
func TestSetNameRoundTripsThroughTheSessionsTable(t *testing.T) {
	r, ctx := newTestRepo(t)
	for _, id := range []string{"sess-1", "sess-2"} {
		if err := r.Insert(ctx, &Session{ID: id, Name: id, Phone: "628000" + id, LastStatus: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetName(ctx, "sess-1", "Renamed Gateway"); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := findByID(all, "sess-1"); got.Name != "Renamed Gateway" {
		t.Errorf("Name = %q after SetName, want %q", got.Name, "Renamed Gateway")
	}
	if got, _ := findByID(all, "sess-2"); got.Name != "sess-2" {
		t.Errorf("sess-2 Name = %q, want untouched", got.Name)
	}
}

// TestSetGroupCodesRoundTripsAndCachesTheFirstCodeOnGroupCode proves
// SetGroupCodes writes every row into wa_session_groups AND keeps
// wa_sessions.group_code in sync as the first (already-sorted) code —
// 0015's "legacy column stays a cached projection" contract.
func TestSetGroupCodesRoundTripsAndCachesTheFirstCodeOnGroupCode(t *testing.T) {
	r, ctx := newTestRepo(t)
	if err := r.Insert(ctx, &Session{ID: "sess-1", Name: "sess-1", Phone: "628000", LastStatus: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetGroupCodes(ctx, "sess-1", []string{"beta", "alpha"}); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findByID(all, "sess-1")
	if !ok {
		t.Fatal("session sess-1 not found after SetGroupCodes")
	}
	if got.GroupCode != "beta" {
		t.Errorf("GroupCode = %q, want the cached first-written code %q (SetGroupCodes trusts caller ordering)", got.GroupCode, "beta")
	}
	if len(got.GroupCodes) != 2 || got.GroupCodes[0] != "alpha" || got.GroupCodes[1] != "beta" {
		t.Errorf("GroupCodes = %v, want [alpha beta] (All sorts its own load)", got.GroupCodes)
	}

	// Replacing with an empty set clears both the join table and the
	// cached column.
	if err := r.SetGroupCodes(ctx, "sess-1", nil); err != nil {
		t.Fatal(err)
	}
	all, err = r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = findByID(all, "sess-1")
	if got.GroupCode != "" || len(got.GroupCodes) != 0 {
		t.Errorf("GroupCode/GroupCodes = %q/%v after clearing, want empty", got.GroupCode, got.GroupCodes)
	}
}

// TestSetGroupCodesOnlyTouchesTheNamedSessionsRows proves a second
// session's groups survive untouched by the first's SetGroupCodes call —
// the DELETE in SetGroupCodes must be scoped to session_id.
func TestSetGroupCodesOnlyTouchesTheNamedSessionsRows(t *testing.T) {
	r, ctx := newTestRepo(t)
	for _, id := range []string{"sess-1", "sess-2"} {
		if err := r.Insert(ctx, &Session{ID: id, Name: id, Phone: "628000" + id, LastStatus: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetGroupCodes(ctx, "sess-1", []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetGroupCodes(ctx, "sess-2", []string{"beta"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetGroupCodes(ctx, "sess-1", []string{"gamma"}); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := findByID(all, "sess-2")
	if len(s2.GroupCodes) != 1 || s2.GroupCodes[0] != "beta" {
		t.Errorf("sess-2 GroupCodes = %v, want untouched [beta]", s2.GroupCodes)
	}
}

// TestSetSelfReminderIsExclusiveAcrossRows proves the repository's own
// CASE WHEN statement, not just the in-memory mirror internal/wa also
// keeps, ends with exactly one row flagged after moving the flag.
func TestSetSelfReminderIsExclusiveAcrossRows(t *testing.T) {
	r, ctx := newTestRepo(t)
	for _, id := range []string{"sess-1", "sess-2", "sess-3"} {
		if err := r.Insert(ctx, &Session{ID: id, Name: id, Phone: "628000" + id, LastStatus: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetSelfReminder(ctx, "sess-1", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSelfReminder(ctx, "sess-2", true); err != nil {
		t.Fatal(err)
	}
	all, err := r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var flagged []string
	for _, s := range all {
		if s.IsSelfReminder {
			flagged = append(flagged, s.ID)
		}
	}
	if len(flagged) != 1 || flagged[0] != "sess-2" {
		t.Fatalf("rows flagged self-reminder = %v, want exactly [sess-2]", flagged)
	}

	if err := r.SetSelfReminder(ctx, "sess-2", false); err != nil {
		t.Fatal(err)
	}
	all, err = r.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.IsSelfReminder {
			t.Fatalf("session %s still flagged after SetSelfReminder(sess-2, false)", s.ID)
		}
	}
}
