package sessionalert

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

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

// TestGetOnAFreshDatabaseReturnsAndPersistsTheDefaults proves the very
// first Get (no row exists yet) returns DefaultSettings() and leaves a
// real row behind, so a second Get sees the same thing without creating a
// second row.
func TestGetOnAFreshDatabaseReturnsAndPersistsTheDefaults(t *testing.T) {
	r, ctx := newTestRepo(t)
	got, err := r.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultSettings()
	if got.DailyTime != want.DailyTime || got.Timezone != want.Timezone {
		t.Errorf("Get on empty table = %+v, want defaults %+v", got, want)
	}
	if got.LogoutAlertEnabled || got.DailyReportEnabled {
		t.Errorf("fresh settings must start with both alerts disabled, got %+v", got)
	}

	again, err := r.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Errorf("second Get = %+v, want identical to first %+v (no duplicate row)", again, got)
	}
}

// TestSaveRoundTripsEveryField proves Save -> Get carries every column,
// including the UPDATE-then-INSERT-fallback path (Save is called directly
// here with no prior Get, so the row does not exist yet).
func TestSaveRoundTripsEveryField(t *testing.T) {
	r, ctx := newTestRepo(t)
	in := Settings{
		LogoutAlertEnabled: true, DailyReportEnabled: true,
		DailyTime: "09:30", Timezone: "Asia/Jakarta",
		TargetGroupJID: "120363xxx@g.us", TargetGroupName: "Ops",
		SenderSessionID: "sess-1",
	}
	if err := r.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogoutAlertEnabled != in.LogoutAlertEnabled || got.DailyReportEnabled != in.DailyReportEnabled ||
		got.DailyTime != in.DailyTime || got.Timezone != in.Timezone ||
		got.TargetGroupJID != in.TargetGroupJID || got.TargetGroupName != in.TargetGroupName ||
		got.SenderSessionID != in.SenderSessionID {
		t.Errorf("Get after Save = %+v, want fields matching %+v", got, in)
	}

	// A second Save (now the row exists) must UPDATE, not duplicate.
	in.TargetGroupName = "Ops Renamed"
	if err := r.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetGroupName != "Ops Renamed" {
		t.Errorf("TargetGroupName = %q after second Save, want %q", got.TargetGroupName, "Ops Renamed")
	}
}

// TestClaimDailyDateIsClaimableOnlyOnce proves the exact anti-double-send
// contract: claiming "2026-11-02" succeeds once, and a second attempt for
// the SAME date fails (returns false, no error) — a later date can still
// be claimed.
func TestClaimDailyDateIsClaimableOnlyOnce(t *testing.T) {
	r, ctx := newTestRepo(t)
	if _, err := r.Get(ctx); err != nil { // ensure the row exists first
		t.Fatal(err)
	}

	claimed, err := r.ClaimDailyDate(ctx, "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("first ClaimDailyDate(2026-11-02) = false, want true")
	}

	claimed, err = r.ClaimDailyDate(ctx, "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second ClaimDailyDate(2026-11-02) = true, want false — must not claim the same date twice")
	}

	claimed, err = r.ClaimDailyDate(ctx, "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("ClaimDailyDate(2026-11-03) = false, want true — a later date must still be claimable")
	}
}
