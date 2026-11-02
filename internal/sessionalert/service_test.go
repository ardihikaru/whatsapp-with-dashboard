package sessionalert

import (
	"context"
	"strings"
	"testing"
	"time"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type clockBox struct{ t time.Time }

func (c *clockBox) now() time.Time          { return c.t }
func (c *clockBox) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T, settings sa.Settings, gw *fakeGateway) (*Service, *clockBox, chan wa.StateChange) {
	t.Helper()
	clk := &clockBox{t: time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)}
	changes := make(chan wa.StateChange, 64)
	svc := New(newFakeSettings(settings), gw, changes, Options{
		Now: clk.now, BatchWindow: 30 * time.Second, Cooldown: 30 * time.Minute,
		MissedSlotGrace: 10 * time.Minute,
	})
	return svc, clk, changes
}

func logoutEnabledSettings() sa.Settings {
	return sa.Settings{LogoutAlertEnabled: true, TargetGroupJID: "120363xxx@g.us", Timezone: "Asia/Jakarta", DailyTime: "08:00"}
}

// TestLogoutAlertsAreBatchedIntoOneMessage proves two logged_out
// transitions arriving within the batch window produce exactly ONE send
// naming both sessions — the 2026-09-30 incident (six sessions within two
// minutes) this collector exists to absorb.
func TestLogoutAlertsAreBatchedIntoOneMessage(t *testing.T) {
	gw := newFakeGateway(Snapshot{ID: "sender", Connected: true, SelfReminder: true})
	svc, clk, changes := newTestService(t, logoutEnabledSettings(), gw)
	ctx := context.Background()

	changes <- wa.StateChange{ID: "a", Name: "Alpha", Phone: "6281", From: wa.StateDisconnected, To: wa.StateLoggedOut, At: clk.now()}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 0 {
		t.Fatalf("sent %d message(s) before the batch window elapsed, want 0", gw.sentCount())
	}

	clk.advance(10 * time.Second)
	changes <- wa.StateChange{ID: "b", Name: "Beta", Phone: "6282", From: wa.StateDisconnected, To: wa.StateLoggedOut, At: clk.now()}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 0 {
		t.Fatalf("sent %d message(s) still within the batch window, want 0", gw.sentCount())
	}

	clk.advance(25 * time.Second) // 35s since "a" arrived: window elapsed
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d message(s) after the batch window elapsed, want exactly 1", gw.sentCount())
	}
	text := gw.lastSend().Text
	if !strings.Contains(text, "Alpha") || !strings.Contains(text, "Beta") {
		t.Errorf("batched alert does not name both sessions; got:\n%s", text)
	}
}

// TestLogoutAlertCooldownSuppressesARepeatForTheSameSession proves a
// SECOND logged_out for the same session inside its cooldown window does
// not produce a second alert.
func TestLogoutAlertCooldownSuppressesARepeatForTheSameSession(t *testing.T) {
	gw := newFakeGateway(Snapshot{ID: "sender", Connected: true, SelfReminder: true})
	svc, clk, changes := newTestService(t, logoutEnabledSettings(), gw)
	ctx := context.Background()

	changes <- wa.StateChange{ID: "a", Name: "Alpha", Phone: "6281", From: wa.StateDisconnected, To: wa.StateLoggedOut, At: clk.now()}
	svc.Tick(ctx)
	clk.advance(31 * time.Second)
	svc.Tick(ctx) // flushes batch 1
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d after first flush, want 1", gw.sentCount())
	}

	// Same session logs out again, well inside the 30-minute cooldown.
	clk.advance(time.Minute)
	changes <- wa.StateChange{ID: "a", Name: "Alpha", Phone: "6281", From: wa.StateDisconnected, To: wa.StateLoggedOut, At: clk.now()}
	svc.Tick(ctx)
	clk.advance(31 * time.Second)
	svc.Tick(ctx)
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d after a repeat within cooldown, want still 1 (suppressed)", gw.sentCount())
	}

	// Past the cooldown, the same session logging out again DOES alert.
	clk.advance(31 * time.Minute)
	changes <- wa.StateChange{ID: "a", Name: "Alpha", Phone: "6281", From: wa.StateDisconnected, To: wa.StateLoggedOut, At: clk.now()}
	svc.Tick(ctx)
	clk.advance(31 * time.Second)
	svc.Tick(ctx)
	if gw.sentCount() != 2 {
		t.Fatalf("sent %d after the cooldown expired, want 2", gw.sentCount())
	}
}

func dailyEnabledSettings() sa.Settings {
	return sa.Settings{DailyReportEnabled: true, TargetGroupJID: "120363xxx@g.us", Timezone: "Asia/Jakarta", DailyTime: "08:00"}
}

// jakartaTime builds a UTC instant corresponding to the given WIB
// (UTC+7) wall-clock time, so test setup reads naturally against
// DailyTime "08:00" Asia/Jakarta.
func jakartaTime(y int, m time.Month, d, hh, mm int) time.Time {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	return time.Date(y, m, d, hh, mm, 0, 0, loc).UTC()
}

// TestDailyReportFiresOnceAtTheConfiguredLocalTime proves the report goes
// out once the slot is reached, and a SECOND tick the same day does not
// send again.
func TestDailyReportFiresOnceAtTheConfiguredLocalTime(t *testing.T) {
	gw := newFakeGateway(
		Snapshot{ID: "sender", Connected: true, SelfReminder: true},
		Snapshot{ID: "down", Name: "Down", Phone: "6283", Connected: false, Active: true, State: "disconnected"},
	)
	svc, clk, _ := newTestService(t, dailyEnabledSettings(), gw)
	clk.t = jakartaTime(2026, 11, 2, 7, 59) // before the 08:00 slot
	ctx := context.Background()

	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 0 {
		t.Fatalf("sent %d before the slot, want 0", gw.sentCount())
	}

	clk.t = jakartaTime(2026, 11, 2, 8, 1) // past the slot
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d at/after the slot, want exactly 1", gw.sentCount())
	}

	clk.advance(time.Hour) // later the SAME day
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d after a second tick the same day, want still 1 (claimed once)", gw.sentCount())
	}
}

// TestDailyReportSkipsAMissedSlotWithoutBackfilling proves a slot
// discovered more than MissedSlotGrace late is skipped — never sent —
// exactly like internal/statussched's own "not back-filled" rule.
func TestDailyReportSkipsAMissedSlotWithoutBackfilling(t *testing.T) {
	gw := newFakeGateway(Snapshot{ID: "sender", Connected: true, SelfReminder: true})
	svc, clk, _ := newTestService(t, dailyEnabledSettings(), gw)
	clk.t = jakartaTime(2026, 11, 2, 9, 0) // 1h after the 08:00 slot — beyond the 10m grace
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 0 {
		t.Fatalf("sent %d for a slot missed by 1h, want 0 (skipped, not back-filled)", gw.sentCount())
	}

	// The date is still claimed (so it is not retried later today either).
	clk.advance(time.Hour)
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 0 {
		t.Fatalf("sent %d on a later same-day tick after a missed slot, want 0", gw.sentCount())
	}
}

// TestDailyReportWithNothingToReportSaysSoCheerfully proves the "every
// session connected" branch, not an empty/garbled message.
func TestDailyReportWithNothingToReportSaysSoCheerfully(t *testing.T) {
	gw := newFakeGateway(
		Snapshot{ID: "a", Connected: true, SelfReminder: true},
		Snapshot{ID: "b", Connected: true},
	)
	svc, clk, _ := newTestService(t, dailyEnabledSettings(), gw)
	clk.t = jakartaTime(2026, 11, 2, 8, 5)
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gw.sentCount() != 1 {
		t.Fatalf("sent %d, want exactly 1", gw.sentCount())
	}
	text := gw.lastSend().Text
	if !strings.Contains(text, "All 2 sessions connected") {
		t.Errorf("report with nothing to report does not say so; got:\n%s", text)
	}
}

// TestSenderFallbackOrderIsConfiguredThenSelfReminderThenAnyConnected
// drives chooseSender's three tiers directly.
func TestSenderFallbackOrderIsConfiguredThenSelfReminderThenAnyConnected(t *testing.T) {
	snaps := []Snapshot{
		{ID: "configured", Connected: true},
		{ID: "self-reminder", Connected: true, SelfReminder: true},
		{ID: "plain", Connected: true},
	}
	if got, ok := chooseSender(snaps, "configured", nil); !ok || got.ID != "configured" {
		t.Errorf("chooseSender with a configured id = %q/%v, want configured/true", got.ID, ok)
	}
	if got, ok := chooseSender(snaps, "", nil); !ok || got.ID != "self-reminder" {
		t.Errorf("chooseSender with no configured id = %q/%v, want self-reminder/true", got.ID, ok)
	}
	noSelfReminder := []Snapshot{{ID: "plain", Connected: true}}
	if got, ok := chooseSender(noSelfReminder, "", nil); !ok || got.ID != "plain" {
		t.Errorf("chooseSender with no configured id and no self-reminder = %q/%v, want plain/true", got.ID, ok)
	}
}

// TestSenderIsNeverAReportedSession proves the excluded set is honoured
// even when the configured sender IS one of the sessions being reported —
// it must fall through to the next tier instead.
func TestSenderIsNeverAReportedSession(t *testing.T) {
	snaps := []Snapshot{
		{ID: "configured", Connected: true},
		{ID: "self-reminder", Connected: true, SelfReminder: true},
	}
	excluded := map[string]bool{"configured": true, "self-reminder": true}
	got, ok := chooseSender(snaps, "configured", excluded)
	if ok {
		t.Fatalf("chooseSender returned %q despite every connected session being excluded, want no candidate", got.ID)
	}

	// With only the configured one excluded, the self-reminder session
	// still wins — never falls through to "configured" just because it
	// was asked for.
	got, ok = chooseSender(snaps, "configured", map[string]bool{"configured": true})
	if !ok || got.ID != "self-reminder" {
		t.Errorf("chooseSender with configured excluded = %q/%v, want self-reminder/true", got.ID, ok)
	}
}
