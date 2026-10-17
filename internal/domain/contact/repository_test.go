package contact

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
	clock := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	return repo
}

func TestInsertAndGetRoundTripAContact(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	c := &Contact{ID: "c1", Name: "Ria", Phone: "6281200000001", Aliases: "ria,ria <3"}
	if err := repo.Insert(ctx, c); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Ria" || got.Phone != "6281200000001" || got.Aliases != "ria,ria <3" {
		t.Errorf("Get returned %+v, want the inserted fields back", got)
	}
	if got.WAStatus != StatusUnknown {
		t.Errorf("WAStatus = %q, want the default %q", got.WAStatus, StatusUnknown)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("CreatedAt/UpdatedAt were not stamped from the injected clock")
	}
}

// TestInsertRejectsADuplicatePhone is the RED-without-the-change test for
// ErrDuplicate: without isDuplicateErr's translation, the second Insert
// would return SQLite's raw driver error instead of the typed sentinel
// callers (the contacts create handler) are meant to match on.
func TestInsertRejectsADuplicatePhone(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	first := &Contact{ID: "c1", Name: "Ria", Phone: "6281200000001"}
	if err := repo.Insert(ctx, first); err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	second := &Contact{ID: "c2", Name: "Ria (dup)", Phone: "6281200000001"}
	err := repo.Insert(ctx, second)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Insert with the same phone returned %v, want ErrDuplicate", err)
	}
}

func TestListReturnsEveryContactNewestFirst(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, &Contact{ID: "c1", Name: "A", Phone: "6281200000001"}); err != nil {
		t.Fatalf("insert c1: %v", err)
	}
	if err := repo.Insert(ctx, &Contact{ID: "c2", Name: "B", Phone: "6281200000002"}); err != nil {
		t.Fatalf("insert c2: %v", err)
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %d contacts, want 2", len(got))
	}
}

func TestDeleteRemovesTheRowAndErrNotFoundOnASecondDelete(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, &Contact{ID: "c1", Name: "A", Phone: "6281200000001"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Delete(ctx, "c1"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if _, err := repo.Get(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete returned %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete returned %v, want ErrNotFound", err)
	}
}

func TestUpdateChangesNameAndAliases(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, &Contact{ID: "c1", Name: "A", Phone: "6281200000001"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Update(ctx, "c1", "A Renamed", "nickname"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "A Renamed" || got.Aliases != "nickname" {
		t.Errorf("Get after Update = %+v, want name/aliases updated", got)
	}
}

// TestSetVerificationRecordsStatusAndJID is the RED-without-the-change test
// for SetVerification: without it, a Verify action would have nowhere to
// persist waxum's verdict and GetKnownGroups-style contacts would stay
// "unknown" forever even after a real check.
func TestSetVerificationRecordsStatusAndJID(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, &Contact{ID: "c1", Name: "Ria", Phone: "6281200000001"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	if err := repo.SetVerification(ctx, "c1", "6281200000001@s.whatsapp.net", StatusRegistered, at); err != nil {
		t.Fatalf("SetVerification: %v", err)
	}
	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WAStatus != StatusRegistered {
		t.Errorf("WAStatus = %q, want %q", got.WAStatus, StatusRegistered)
	}
	if got.JID != "6281200000001@s.whatsapp.net" {
		t.Errorf("JID = %q, want the resolved jid", got.JID)
	}
	if !got.VerifiedAt.Equal(at) {
		t.Errorf("VerifiedAt = %v, want %v", got.VerifiedAt, at)
	}
}

// TestSetVerificationNeverWritesNotRegisteredOnAFailurePath documents the
// handler-layer rule (see contacts_check.go's contactVerdict doc comment)
// at the repository boundary: a backend-failure path must call
// SetVerification with StatusUnknown, not fabricate StatusNotRegistered —
// this test only pins that the repository itself stores whatever status
// it is given faithfully, it does not invent the distinction.
func TestSetVerificationNeverWritesNotRegisteredOnAFailurePath(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, &Contact{ID: "c1", Name: "Ria", Phone: "6281200000001"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.SetVerification(ctx, "c1", "", StatusUnknown, time.Time{}); err != nil {
		t.Fatalf("SetVerification: %v", err)
	}
	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WAStatus != StatusUnknown {
		t.Errorf("WAStatus = %q, want %q", got.WAStatus, StatusUnknown)
	}
}
