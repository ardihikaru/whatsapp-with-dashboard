package statussched

import (
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
)

type fakeGW struct {
	connected bool
	reason    string
	contacts  []string
	postErr   error
	posts     []Post
}

func (g *fakeGW) Connected(string) (bool, string) { return g.connected, g.reason }
func (g *fakeGW) ContactJIDs(context.Context) ([]string, error) {
	return g.contacts, nil
}
func (g *fakeGW) Post(_ context.Context, p Post) (string, error) {
	g.posts = append(g.posts, p)
	if g.postErr != nil {
		return "", g.postErr
	}
	return "wamid-1", nil
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	repo *sched.Repository
	gw   *fakeGW
	s    *Scheduler
	now  time.Time
}

// 2026-11-02 is a Monday. 00:00Z = 07:00 in Asia/Jakarta (UTC+7).
func newHarness(t *testing.T, now time.Time) *harness {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: context.Background(), repo: sched.NewRepository(pool),
		gw: &fakeGW{connected: true, contacts: []string{"1@s.whatsapp.net", "2@s.whatsapp.net"}}, now: now}
	h.s = New(h.repo, h.gw, Options{Now: func() time.Time { return h.now }, Rand: rand.New(rand.NewPCG(7, 9))})
	return h
}

func (h *harness) add(id string, mut func(*sched.Schedule)) {
	sc := &sched.Schedule{
		ID: id, Name: id, SessionID: "s1", Kind: sched.KindText, Text: "hello",
		Weekdays: "1,3", WindowStart: "07:00", WindowEnd: "10:27", Timezone: "Asia/Jakarta",
		StartDate: "2026-11-01", RecipientsMode: sched.ModeAll, State: sched.StateActive,
	}
	if mut != nil {
		mut(sc)
	}
	if err := h.repo.Insert(h.ctx, sc); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.s.Tick(h.ctx); err != nil {
		h.t.Fatalf("Tick: %v", err)
	}
}

func (h *harness) get(id string) sched.Schedule {
	h.t.Helper()
	sc, err := h.repo.Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sc
}

func utc(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
}

func TestPlansOnlySelectedWeekdaysAndDrawsInsideTheWindow(t *testing.T) {
	// Monday 06:00 WIB: window 07:00-10:27 WIB is entirely ahead.
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	sc := h.get("a")
	if sc.NextFireAt.IsZero() {
		t.Fatal("Monday is selected but nothing was planned")
	}
	lo, hi := utc(2026, 11, 2, 0, 0), utc(2026, 11, 2, 3, 27)
	if sc.NextFireAt.Before(lo) || sc.NextFireAt.After(hi) {
		t.Errorf("planned %v outside the 07:00-10:27 WIB window [%v, %v]", sc.NextFireAt, lo, hi)
	}

	// Tuesday is not selected: nothing planned.
	h2 := newHarness(t, utc(2026, 11, 2, 23, 0)) // Tue 06:00 WIB
	h2.add("b", nil)
	h2.tick()
	if got := h2.get("b"); !got.NextFireAt.IsZero() || got.LastPlannedDate != "" {
		t.Errorf("Tuesday must plan nothing, got %+v", got)
	}
}

func TestDoesNotPlanTwiceInOneDay(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	first := h.get("a").NextFireAt
	h.now = h.now.Add(time.Minute)
	h.tick()
	if got := h.get("a").NextFireAt; !got.Equal(first) {
		t.Errorf("re-planned the same day: %v then %v", first, got)
	}
}

func TestSkipsThePastPartOfTheWindowAndNeverPlansBeforeNow(t *testing.T) {
	// 09:00 WIB Monday: only 09:00-10:27 remains.
	h := newHarness(t, utc(2026, 11, 2, 2, 0))
	h.add("a", nil)
	h.tick()
	at := h.get("a").NextFireAt
	if at.Before(h.now) || at.After(utc(2026, 11, 2, 3, 27)) {
		t.Errorf("planned %v, want within [now, 10:27 WIB]", at)
	}
}

func TestSkipsTodayWhenTheWindowIsAlreadyOver(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 2, 5, 0)) // 12:00 WIB
	h.add("a", nil)
	h.tick()
	sc := h.get("a")
	if !sc.NextFireAt.IsZero() || sc.LastPlannedDate != "2026-11-02" {
		t.Errorf("late window must be skipped and marked planned, got next=%v planned=%q", sc.NextFireAt, sc.LastPlannedDate)
	}
	if len(h.gw.posts) != 0 {
		t.Error("a post was back-filled")
	}
}

func TestFinishesAfterEndDateAndNeverPostsBeforeStartDate(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 2, 0, 0))
	h.add("ended", func(s *sched.Schedule) { s.EndDate = "2026-11-01" })
	h.add("future", func(s *sched.Schedule) { s.StartDate = "2026-11-09" })
	h.tick()
	if got := h.get("ended").State; got != sched.StateFinished {
		t.Errorf("past end_date state = %q, want finished", got)
	}
	if got := h.get("future"); got.State != sched.StateActive || !got.NextFireAt.IsZero() {
		t.Errorf("before start_date must stay idle, got %+v", got)
	}
}

func TestFiresOnceWhenDueResolvesAllContactsAtFireTime(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	h.gw.contacts = append(h.gw.contacts, "3@s.whatsapp.net") // added AFTER the schedule was saved/planned
	h.now = h.get("a").NextFireAt.Add(time.Second)
	h.tick()
	h.tick()
	if len(h.gw.posts) != 1 {
		t.Fatalf("posts = %d, want exactly 1", len(h.gw.posts))
	}
	if got := h.gw.posts[0].Recipients; len(got) != 3 {
		t.Errorf("recipients = %v, want all 3 contacts incl. the one added later", got)
	}
	sc := h.get("a")
	if sc.LastResult != sched.ResultSent || sc.LastError != "" || !sc.NextFireAt.IsZero() {
		t.Errorf("after a successful fire: %+v", sc)
	}
}

func TestCustomRecipientsAreUsedVerbatim(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", func(s *sched.Schedule) {
		s.RecipientsMode, s.RecipientsJSON = sched.ModeCustom, `["9@s.whatsapp.net"]`
	})
	h.tick()
	h.now = h.get("a").NextFireAt
	h.tick()
	if len(h.gw.posts) != 1 || len(h.gw.posts[0].Recipients) != 1 || h.gw.posts[0].Recipients[0] != "9@s.whatsapp.net" {
		t.Fatalf("posts = %+v", h.gw.posts)
	}
}

func TestAFailedPostIsRecordedAndNeverRetried(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt
	h.gw.postErr = errors.New("waxum said no")
	h.tick()
	h.now = h.now.Add(time.Minute)
	h.tick()
	h.tick()
	if len(h.gw.posts) != 1 {
		t.Fatalf("posts = %d, want 1 (no auto-retry)", len(h.gw.posts))
	}
	sc := h.get("a")
	if sc.LastResult != sched.ResultFailed || sc.LastError != "waxum said no" {
		t.Errorf("failure not recorded: %+v", sc)
	}
	if sc.State != sched.StateActive {
		t.Errorf("a failed post must not deactivate the schedule, state = %q", sc.State)
	}
}

func TestADisconnectedSessionIsSkippedNotPosted(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt
	h.gw.connected, h.gw.reason = false, "session not connected"
	h.tick()
	if len(h.gw.posts) != 0 {
		t.Fatal("posted through a disconnected session")
	}
	sc := h.get("a")
	if sc.LastResult != sched.ResultSkipped || sc.LastError != "skipped: session not connected" {
		t.Errorf("skip not recorded: %+v", sc)
	}
}

func TestAPlannedPostMissedByTooLongIsSkippedNotBackFilled(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt.Add(MaxLateness + time.Minute) // app was down
	h.tick()
	if len(h.gw.posts) != 0 {
		t.Fatal("a stale slot was back-filled")
	}
	if got := h.get("a").LastResult; got != sched.ResultSkipped {
		t.Errorf("LastResult = %q, want skipped", got)
	}
}

func TestNoRecipientsFailsInsteadOfPostingToNobody(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.gw.contacts = nil
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt
	h.tick()
	if len(h.gw.posts) != 0 || h.get("a").LastResult != sched.ResultFailed {
		t.Errorf("empty contact list must fail without posting; posts=%d sc=%+v", len(h.gw.posts), h.get("a"))
	}
}

func TestEditingAfterTodaysPostDoesNotPostASecondTime(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt
	h.tick() // posts
	sc := h.get("a")
	if err := h.repo.Update(h.ctx, &sc); err != nil { // resets planning
		t.Fatal(err)
	}
	h.now = h.now.Add(time.Minute)
	h.tick() // would plan a fresh slot without the already-posted-today guard
	if next := h.get("a").NextFireAt; !next.IsZero() {
		h.now = next
	}
	h.tick()
	if len(h.gw.posts) != 1 {
		t.Errorf("posts = %d after an edit the same day, want still 1", len(h.gw.posts))
	}
}

func TestPausedSchedulesAreNeitherPlannedNorFired(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", func(s *sched.Schedule) { s.State = sched.StatePaused })
	h.tick()
	if !h.get("a").NextFireAt.IsZero() {
		t.Error("a paused schedule was planned")
	}
}

func TestAnUnknownTimezoneStallsTheScheduleInsteadOfCrashing(t *testing.T) {
	h := newHarness(t, utc(2026, 11, 1, 23, 0))
	h.add("a", func(s *sched.Schedule) { s.Timezone = "Mars/Olympus" })
	h.tick()
	if got := h.get("a"); got.State != sched.StateStalled || got.PausedReason == "" {
		t.Errorf("want stalled with a reason, got %+v", got)
	}
}

func TestPlanUsesTheScheduleTimezoneForTheLocalDay(t *testing.T) {
	// 20:00Z Sunday is 03:00 MONDAY in Jakarta: a Monday-only schedule plans.
	h := newHarness(t, utc(2026, 11, 1, 20, 0))
	h.add("a", func(s *sched.Schedule) { s.Weekdays = "1" })
	h.tick()
	if h.get("a").NextFireAt.IsZero() {
		t.Error("Monday in Jakarta (Sunday in UTC) was not planned")
	}
}
