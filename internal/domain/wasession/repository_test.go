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
