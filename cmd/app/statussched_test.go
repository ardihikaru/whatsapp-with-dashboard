package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestStartStatusSchedulesReturnsARunningSchedulerWithNoEnablingFlag pins
// both halves of "always on": there is no config flag to pass, and the loop
// is already running when the function returns. A schedule whose planned
// slot is hours stale is claimed and recorded "skipped" by the loop's FIRST
// tick (it never reaches the session, so no backend is needed); if Start
// were not called, LastResult would stay empty.
func TestStartStatusSchedulesReturnsARunningSchedulerWithNoEnablingFlag(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := sched.NewRepository(pool)
	stale := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	sc := &sched.Schedule{
		ID: "s", Name: "n", SessionID: "gone", Kind: sched.KindText, Text: "t", Weekdays: "1,2,3,4,5,6,7",
		WindowStart: "00:00", WindowEnd: "23:59", Timezone: "UTC", StartDate: "2020-01-01",
		RecipientsMode: sched.ModeAll, State: sched.StateActive, NextFireAt: stale,
		LastPlannedDate: time.Now().UTC().Format("2006-01-02"), // already planned today, so only the fire pass touches it
	}
	if err := repo.Insert(ctx, sc); err != nil {
		t.Fatal(err)
	}
	mgr := wa.NewManager(ctx, nil, wasession.NewRepository(pool), message.NewRepository(pool), nil,
		wa.Config{SendRate: 1, SendBurst: 1, ReconcilePar: 1})

	s := startStatusSchedules(ctx, repo, mgr, nil, nil)
	if s == nil {
		t.Fatal("startStatusSchedules returned nil")
	}
	defer s.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := repo.Get(ctx, "s")
		if err != nil {
			t.Fatal(err)
		}
		if got.LastResult == sched.ResultSkipped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the scheduler loop never ticked: LastResult is still empty after 3s")
}
