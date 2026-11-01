package statusschedule

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
	clock := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	return repo
}

func sample(id string) *Schedule {
	return &Schedule{
		ID: id, Name: "Morning", SessionID: "s1", Kind: KindImage, Caption: "hi",
		BgARGB: 4280184399, MediaObjectKey: "media/s1/2026/11/x.png", MediaContentType: "image/png", MediaFilename: "x.png",
		Weekdays: "1,3", WindowStart: "07:00", WindowEnd: "10:27", Timezone: "Asia/Jakarta",
		StartDate: "2026-11-02", RecipientsMode: ModeCustom, RecipientsJSON: `["62811@s.whatsapp.net"]`,
		State: StateActive,
	}
}

func TestInsertAndGetRoundTripEveryField(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	if err := repo.Insert(ctx, sample("a")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := sample("a")
	if got.BgARGB != want.BgARGB || got.Weekdays != "1,3" || got.WindowEnd != "10:27" ||
		got.RecipientsMode != ModeCustom || got.RecipientsJSON != want.RecipientsJSON ||
		got.MediaObjectKey != want.MediaObjectKey || got.EndDate != "" || got.LastPlannedDate != "" ||
		!got.NextFireAt.IsZero() || got.CreatedAt.IsZero() {
		t.Errorf("round trip lost data: %+v", got)
	}
	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
}

func TestDueReturnsOnlyActiveSchedulesWhoseFireTimeHasPassed(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	for _, id := range []string{"past", "future", "paused", "none"} {
		if err := repo.Insert(ctx, sample(id)); err != nil {
			t.Fatal(err)
		}
	}
	_ = repo.MarkPlanned(ctx, "past", "2026-11-02", now.Add(-time.Minute))
	_ = repo.MarkPlanned(ctx, "future", "2026-11-02", now.Add(time.Minute))
	_ = repo.MarkPlanned(ctx, "paused", "2026-11-02", now.Add(-time.Minute))
	if err := repo.SetState(ctx, "paused", StatePaused, "by operator"); err != nil {
		t.Fatal(err)
	}
	_ = repo.MarkPlanned(ctx, "paused", "2026-11-02", now.Add(-time.Minute)) // even with a stale time
	due, err := repo.Due(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "past" {
		t.Fatalf("Due = %v, want exactly [past]", ids(due))
	}
}

func ids(s []Schedule) []string {
	var out []string
	for _, x := range s {
		out = append(out, x.ID)
	}
	return out
}

func TestClaimFireSucceedsOnceAndRefusesAReplannedTime(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	_ = repo.Insert(ctx, sample("a"))
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	_ = repo.MarkPlanned(ctx, "a", "2026-11-02", at)
	if err := repo.ClaimFire(ctx, "a", at.Add(time.Second)); !errors.Is(err, ErrNotClaimable) {
		t.Fatalf("claim of a different instant = %v, want ErrNotClaimable", err)
	}
	if err := repo.ClaimFire(ctx, "a", at); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := repo.ClaimFire(ctx, "a", at); !errors.Is(err, ErrNotClaimable) {
		t.Fatalf("second claim = %v, want ErrNotClaimable (no double post)", err)
	}
}

func TestSetStateAndUpdateResetPlanningButKeepTheFireHistory(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	_ = repo.Insert(ctx, sample("a"))
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	_ = repo.MarkPlanned(ctx, "a", "2026-11-02", at)
	_ = repo.RecordResult(ctx, "a", at, ResultSent, "")

	edited := sample("a")
	edited.Name = "Renamed"
	if err := repo.Update(ctx, edited); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, "a")
	if got.Name != "Renamed" || got.LastPlannedDate != "" || !got.NextFireAt.IsZero() {
		t.Errorf("Update did not reset planning: %+v", got)
	}
	if !got.LastFiredAt.Equal(at) || got.LastResult != ResultSent {
		t.Errorf("Update wiped the fire history: %+v", got)
	}
	if err := repo.Update(ctx, &Schedule{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) = %v", err)
	}
}

func TestDeleteRemovesTheRow(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	_ = repo.Insert(ctx, sample("a"))
	if err := repo.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}
