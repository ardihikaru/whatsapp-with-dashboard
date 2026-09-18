package knowngroup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	repo := NewRepository(pool)
	clock := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	return repo
}

func TestInsertAndGetRoundTripAGroup(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	g := &Group{ID: "kg1", SessionID: "sess-a", GroupJID: "120363426585371342@g.us", Name: "Clino Team"}
	if err := repo.Insert(ctx, g); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.Get(ctx, "kg1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Clino Team" || got.GroupJID != "120363426585371342@g.us" || got.SessionID != "sess-a" {
		t.Errorf("Get returned %+v, want the inserted fields back", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("CreatedAt/UpdatedAt were not stamped from the injected clock")
	}
}

// TestInsertRejectsADuplicateSessionAndJIDPair is the RED-without-the-change
// test for ErrDuplicate: without isDuplicateErr's translation, the second
// Insert would return SQLite's raw driver error instead of the typed
// sentinel callers (the known-groups create handler) are meant to match on.
func TestInsertRejectsADuplicateSessionAndJIDPair(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	first := &Group{ID: "kg1", SessionID: "sess-a", GroupJID: "120363426585371342@g.us", Name: "Clino Team"}
	if err := repo.Insert(ctx, first); err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	second := &Group{ID: "kg2", SessionID: "sess-a", GroupJID: "120363426585371342@g.us", Name: "Clino Team (dup)"}
	err := repo.Insert(ctx, second)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Insert with the same (session_id, group_jid) pair returned %v, want ErrDuplicate", err)
	}
}

func TestListBySessionOnlyReturnsThatSessionsGroups(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, &Group{ID: "kg1", SessionID: "sess-a", GroupJID: "1@g.us", Name: "A1"}); err != nil {
		t.Fatalf("insert kg1: %v", err)
	}
	if err := repo.Insert(ctx, &Group{ID: "kg2", SessionID: "sess-a", GroupJID: "2@g.us", Name: "A2"}); err != nil {
		t.Fatalf("insert kg2: %v", err)
	}
	if err := repo.Insert(ctx, &Group{ID: "kg3", SessionID: "sess-b", GroupJID: "3@g.us", Name: "B1"}); err != nil {
		t.Fatalf("insert kg3: %v", err)
	}

	got, err := repo.ListBySession(ctx, "sess-a")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListBySession(sess-a) returned %d groups, want 2", len(got))
	}
	for _, g := range got {
		if g.SessionID != "sess-a" {
			t.Errorf("ListBySession(sess-a) returned a row for session %q", g.SessionID)
		}
	}
}

func TestDeleteRemovesTheRowAndErrNotFoundOnASecondDelete(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, &Group{ID: "kg1", SessionID: "sess-a", GroupJID: "1@g.us", Name: "A1"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Delete(ctx, "kg1"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if _, err := repo.Get(ctx, "kg1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete returned %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "kg1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete returned %v, want ErrNotFound", err)
	}
}
