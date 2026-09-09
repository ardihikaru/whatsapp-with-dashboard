package selfconvo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"math/rand/v2"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// ---- test plumbing ----

// fakeClock is a manually advanced clock, injected as Options.Now so every
// test controls exactly what the runner considers "now" — no sleeping, no
// flakiness, per CLAUDE.md's "clocks injected as parameters" convention.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeGateway is the runner's positive-path double — see gateway.go's doc
// comment for why the runner is tested against an interface instead of a
// real *wa.Manager. Participants are pre-seeded and mutable mid-test
// (setConnected, for the disconnect-mid-run case); Send records every call
// and optionally fails or blocks on demand.
type fakeGateway struct {
	mu           sync.Mutex
	participants map[string]Participant
	sends        []SendRequest
	sendErr      error
	blockOnCtx   bool // Send blocks until ctx.Done(), then returns ctx.Err()

	// onParticipant, when set, fires ONCE — on the very next Participant
	// call — then clears itself, letting a test inject a side effect at the
	// exact point in processRun's ordering between NextPendingTurns and
	// ClaimTurn: processRun calls Participant(SessionAID) then
	// Participant(SessionBID) after picking the turn but before claiming
	// it, so a hook that fires here reproduces a concurrent Cancel landing
	// in that window deterministically, with no goroutines or timing needed.
	onParticipant func()

	// sendHook, when set, fires on EVERY Send call, before that call's
	// normal blockOnCtx/sendErr handling — the concurrency tests
	// (TestDueRunsOnDifferentPairsAdvanceConcurrently,
	// TestConcurrencyIsBoundedByMaxConcurrentRuns) use it to observe/gate
	// how many sends are in flight at once, deterministically.
	sendHook func(SendRequest)

	// messageStatuses backs MessageStatus — Recover's tests configure it
	// per message id; an id with no entry reports found=false, matching
	// managerGateway's own "no row at all" answer for an id that was never
	// actually inserted, which is exactly the fake's default state (no
	// production Send path runs in these tests unless a test wires one).
	messageStatuses map[string]fakeMessageStatus
}

// fakeMessageStatus is one configured MessageStatus answer.
type fakeMessageStatus struct {
	status string
	reason string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{participants: make(map[string]Participant)}
}

func (g *fakeGateway) seed(p Participant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.participants[p.ID] = p
}

func (g *fakeGateway) setConnected(id string, connected bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.participants[id]
	p.Connected = connected
	g.participants[id] = p
}

func (g *fakeGateway) Participant(id string) (Participant, bool) {
	g.mu.Lock()
	hook := g.onParticipant
	g.onParticipant = nil
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.participants[id]
	return p, ok
}

func (g *fakeGateway) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	g.mu.Lock()
	g.sends = append(g.sends, req)
	n := len(g.sends)
	block := g.blockOnCtx
	sendErr := g.sendErr
	hook := g.sendHook
	g.mu.Unlock()

	if hook != nil {
		hook(req)
	}
	if block {
		<-ctx.Done()
		return SendReceipt{}, ctx.Err()
	}
	if sendErr != nil {
		return SendReceipt{}, sendErr
	}
	return SendReceipt{MessageID: "msg-" + req.RunID + "-" + strconv.Itoa(n)}, nil
}

// setMessageStatus configures MessageStatus's answer for messageID —
// Recover's tests use this to simulate what a crash-mid-send left behind in
// wa_messages.
func (g *fakeGateway) setMessageStatus(messageID, status, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.messageStatuses == nil {
		g.messageStatuses = map[string]fakeMessageStatus{}
	}
	g.messageStatuses[messageID] = fakeMessageStatus{status: status, reason: reason}
}

func (g *fakeGateway) MessageStatus(ctx context.Context, messageID string) (status, reason string, found bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.messageStatuses[messageID]
	if !ok {
		return "", "", false, nil
	}
	return s.status, s.reason, true, nil
}

func (g *fakeGateway) sendCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sends)
}

func (g *fakeGateway) sendAt(i int) SendRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sends[i]
}

func newTestPool(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	return pool
}

// seedExampleTemplate stores the embedded example script (10 turns, both
// speakers, no variation fields — TestValidateAcceptsTheEmbeddedExample
// already pins that it validates) as a template row.
func seedExampleTemplate(t *testing.T, repo *convo.Repository) convo.Template {
	t.Helper()
	script, err := Validate(ExampleJSON)
	if err != nil {
		t.Fatalf("Validate(ExampleJSON): %v", err)
	}
	tmpl := convo.Template{
		ID:        "tmpl-example",
		Name:      script.Name,
		TurnCount: len(script.Turns),
		Body:      string(ExampleJSON),
	}
	if err := repo.InsertTemplate(context.Background(), &tmpl); err != nil {
		t.Fatalf("InsertTemplate: %v", err)
	}
	return tmpl
}

func newDeterministicRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

// newTestRunner wires a Runner over a fresh SQLite pool and fake gateway,
// with sess-a/sess-b both seeded as connected participants — the common
// case; individual tests mutate connectivity or the clock as needed.
func newTestRunner(t *testing.T, clock *fakeClock) (*Runner, *convo.Repository, *fakeGateway) {
	t.Helper()
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	gw := newFakeGateway()
	gw.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	r := New(repo, gw, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	return r, repo, gw
}

// ---- Trigger ----

func TestTriggerRefusesWhenFewerThanTwoTickedSessionsAreConnected(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)
	gw.setConnected("sess-b", false)

	_, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if !errors.Is(err, ErrNotEnoughParticipants) {
		t.Fatalf("err = %v, want ErrNotEnoughParticipants", err)
	}
}

func TestTriggerStoresAScheduledRunWhosePairComesFromTheTickedSet(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if run.State != convo.RunScheduled {
		t.Fatalf("State = %q, want %q", run.State, convo.RunScheduled)
	}
	if run.NextDueAt == nil || !run.NextDueAt.Equal(run.StartAt) {
		t.Fatalf("NextDueAt = %v, want == StartAt (%v)", run.NextDueAt, run.StartAt)
	}
	if run.TurnCount != 10 {
		t.Fatalf("TurnCount = %d, want 10", run.TurnCount)
	}
	ticked := map[string]bool{"sess-a": true, "sess-b": true}
	if !ticked[run.SessionAID] || !ticked[run.SessionBID] {
		t.Fatalf("SessionAID/SessionBID = %q/%q, want both in the ticked set", run.SessionAID, run.SessionBID)
	}
	if run.SessionAID == run.SessionBID {
		t.Fatalf("SessionAID == SessionBID == %q, want distinct sessions", run.SessionAID)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 10 {
		t.Fatalf("len(turns) = %d, want 10", len(turns))
	}
}

// TestTriggerUsesAPreValidatedTemplateWithoutRereadingIt pins the fix for
// "upload-and-trigger validates the same bytes three times"
// (docs/brief-self-convo.md's review follow-ups): when
// TriggerRequest.Template/Script are both set, Trigger must use them
// directly instead of calling repo.GetTemplate(req.TemplateID) +
// Validate(t.Body) — the handler's upload-and-trigger path already ran
// Validate once (readTemplateUpload) and already has the parsed Script in
// hand, so re-fetching and re-parsing the row Trigger's own CreateRun is
// about to insert would be a third validation of bytes checked twice
// already. Proved here by handing Trigger a Template whose ID does NOT
// exist in the repo at all: before this change Trigger unconditionally
// called GetTemplate first and this failed with convo.ErrNotFound; after
// it, the run is still created, named after the template actually passed
// in.
func TestTriggerUsesAPreValidatedTemplateWithoutRereadingIt(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)

	script, err := Validate(ExampleJSON)
	if err != nil {
		t.Fatalf("Validate(ExampleJSON): %v", err)
	}
	tmpl := &convo.Template{
		ID: "unsaved-template-id", Name: "Pre-validated template", TurnCount: len(script.Turns),
	}

	run, err := r.Trigger(context.Background(), TriggerRequest{
		Template: tmpl, Script: &script,
		TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger with a pre-validated template errored (want it to skip GetTemplate entirely): %v", err)
	}
	if run.TemplateID != tmpl.ID {
		t.Errorf("run.TemplateID = %q, want %q", run.TemplateID, tmpl.ID)
	}
	if run.TemplateName != tmpl.Name {
		t.Errorf("run.TemplateName = %q, want %q", run.TemplateName, tmpl.Name)
	}
	if run.TurnCount != 10 {
		t.Fatalf("run.TurnCount = %d, want 10", run.TurnCount)
	}

	if _, err := repo.GetTemplate(context.Background(), tmpl.ID); err == nil {
		t.Error("GetTemplate found a row for an id that was never inserted — Trigger must not have written one")
	}
}

// TestTriggerAppliesVariationSoTwoRunsFromOneTemplateCanDiffer stores ONE
// template whose every turn offers an alternative (plus one {{var}}
// placeholder), triggers it from several runners seeded with different
// Options.Rand over the SAME pool/template, and checks that (a) at least
// two of the triggered runs' stored turn bodies differ somewhere, proving
// Vary actually ran inside Trigger rather than being skipped, and (b) no
// stored body still contains a raw `{{` placeholder, proving substitution
// completed before the turns were persisted.
func TestTriggerAppliesVariationSoTwoRunsFromOneTemplateCanDiffer(t *testing.T) {
	varyRaw := `{
		"version": 1,
		"vars": { "place": ["Bandung", "Bogor"] },
		"turns": [
			{ "speaker": "a", "text": "Halo", "alternatives": ["Hai"] },
			{ "speaker": "b", "text": "Baik", "alternatives": ["Sehat"] },
			{ "speaker": "a", "text": "Mau ke {{place}} gak?" },
			{ "speaker": "b", "text": "Boleh", "alternatives": ["Oke"] }
		]
	}`
	script, err := Validate([]byte(varyRaw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}

	pool := newTestPool(t)
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))

	setupRepo := convo.NewRepository(pool)
	tmpl := convo.Template{
		ID:        "tmpl-vary",
		Name:      script.Name,
		TurnCount: len(script.Turns),
		Body:      varyRaw,
	}
	if err := setupRepo.InsertTemplate(context.Background(), &tmpl); err != nil {
		t.Fatalf("InsertTemplate: %v", err)
	}

	newRunner := func(seed uint64) (*Runner, *convo.Repository) {
		repo := convo.NewRepository(pool)
		gw := newFakeGateway()
		gw.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
		gw.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
		r := New(repo, gw, Options{Now: clock.Now, Rand: rand.New(rand.NewPCG(seed, seed+1)), PollInterval: 5 * time.Second})
		return r, repo
	}

	seen := map[string]bool{}
	for seed := uint64(0); seed < 20; seed++ {
		r, repo := newRunner(seed)
		run, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
		})
		if err != nil {
			t.Fatalf("seed %d: Trigger: %v", seed, err)
		}
		turns, err := repo.Turns(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("seed %d: Turns: %v", seed, err)
		}
		var combined strings.Builder
		for _, tu := range turns {
			if strings.Contains(tu.Body, "{{") {
				t.Fatalf("seed %d: stored turn body still contains a placeholder: %q", seed, tu.Body)
			}
			combined.WriteString(tu.Body)
			combined.WriteByte('|')
		}
		seen[combined.String()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected at least two distinct variations across 20 seeds triggering the same template, got %d distinct: %v", len(seen), seen)
	}
}

// TestToTurnsMirrorsWhatTriggerStores pins ToTurns (the pure helper pulled
// out of Trigger, for a later seeder to reuse without duplicating the
// conversion) against the SAME shape Trigger itself persists: every field
// copied across, RunID stamped on each row, and State forced to
// convo.TurnPending regardless of what ExpandedTurn carries (it doesn't
// have a state field at all — this is ToTurns' own job).
func TestToTurnsMirrorsWhatTriggerStores(t *testing.T) {
	expanded := []ExpandedTurn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "Halo", DelaySeconds: 0},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "Hai", DelaySeconds: 15},
	}

	got := ToTurns("run-xyz", expanded)

	if len(got) != len(expanded) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(expanded))
	}
	for i, et := range expanded {
		want := convo.Turn{
			RunID:        "run-xyz",
			Seq:          et.Seq,
			Speaker:      et.Speaker,
			SessionID:    et.SessionID,
			ToPhone:      et.ToPhone,
			Body:         et.Body,
			DelaySeconds: et.DelaySeconds,
			State:        convo.TurnPending,
		}
		if got[i] != want {
			t.Fatalf("turn %d = %+v, want %+v", i, got[i], want)
		}
	}
}

// ---- Tick timing ----

func TestATurnIsNotSentBeforeItsStartAt(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	startAt := clock.Now().Add(time.Hour)
	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"}, StartAt: startAt,
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := gw.sendCount(); n != 0 {
		t.Fatalf("sendCount = %d, want 0 (start_at is an hour ahead)", n)
	}

	clock.Advance(time.Hour + time.Second)
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount = %d, want 1 after start_at elapses", n)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.TurnsDone != 1 {
		t.Fatalf("TurnsDone = %d, want 1", got.TurnsDone)
	}
}

func TestTicksSendTurnsInSequenceAlternatingBetweenTheTwoChosenSessions(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}

	for i, want := range turns {
		if i > 0 {
			clock.Advance(time.Duration(want.DelaySeconds) * time.Second)
		}
		if _, err := r.Tick(context.Background()); err != nil {
			t.Fatalf("Tick[%d]: %v", i, err)
		}
		if n := gw.sendCount(); n != i+1 {
			t.Fatalf("after turn %d: sendCount = %d, want %d", i+1, n, i+1)
		}
		got := gw.sendAt(i)
		if got.SessionID != want.SessionID {
			t.Fatalf("turn %d: SessionID = %q, want %q", i+1, got.SessionID, want.SessionID)
		}
		if got.ToDigits != want.ToPhone {
			t.Fatalf("turn %d: ToDigits = %q, want %q", i+1, got.ToDigits, want.ToPhone)
		}
		if got.Text != want.Body {
			t.Fatalf("turn %d: Text = %q, want %q", i+1, got.Text, want.Body)
		}
	}

	final, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if final.State != convo.RunDone {
		t.Fatalf("State = %q, want %q", final.State, convo.RunDone)
	}
	if final.TurnsDone != final.TurnCount {
		t.Fatalf("TurnsDone = %d, want %d (== TurnCount)", final.TurnsDone, final.TurnCount)
	}
}

func TestNextDueIsComputedFromTheSendTimeSoAnOutageNeverCausesACatchUpBurst(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}

	// Turn 1: delay always 0, due at StartAt == clock.Now() already.
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (turn 1): %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount after turn 1 = %d, want 1", n)
	}

	// Simulate a long outage: jump the clock 3 hours past turn 2's real due
	// time, far beyond it. One Tick call must send exactly ONE turn (turn
	// 2), not try to catch up on anything.
	clock.Advance(3 * time.Hour)
	sendTimeOfTurn2 := clock.Now()
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (turn 2): %v", err)
	}
	if n := gw.sendCount(); n != 2 {
		t.Fatalf("sendCount after the outage tick = %d, want exactly 2 (one turn sent, no catch-up burst)", n)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	wantNextDue := sendTimeOfTurn2.Add(time.Duration(turns[2].DelaySeconds) * time.Second)
	if got.NextDueAt == nil || !got.NextDueAt.Equal(wantNextDue) {
		t.Fatalf("NextDueAt = %v, want %v (send time of turn 2 + turn 3's delay, not StartAt + cumulative delays)",
			got.NextDueAt, wantNextDue)
	}
}

// ---- concurrency ----

// TestDueRunsOnDifferentPairsAdvanceConcurrently is the RED-first
// regression test for fanning processRun out per due run: two due runs over
// DISJOINT pairs must have their sends IN FLIGHT AT THE SAME TIME, not
// serialize behind each other's gw.Send call. The fake gateway's sendHook
// blocks each send until BOTH have entered, with a bounded timeout, so the
// old strictly-sequential Tick (which never even starts run 2's send until
// run 1's Send call returns) fails this test with a clear message instead
// of hanging it.
func TestDueRunsOnDifferentPairsAdvanceConcurrently(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	gw := newFakeGateway()
	gw.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	gw.seed(Participant{ID: "sess-c", Name: "Session C", Phone: "628110000003", Connected: true})
	gw.seed(Participant{ID: "sess-d", Name: "Session D", Phone: "628110000004", Connected: true})

	r := New(repo, gw, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	tmpl := seedExampleTemplate(t, repo)

	if _, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	}); err != nil {
		t.Fatalf("Trigger (run 1): %v", err)
	}
	if _, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-c", "sess-d"},
	}); err != nil {
		t.Fatalf("Trigger (run 2): %v", err)
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	gw.mu.Lock()
	gw.sendHook = func(req SendRequest) {
		entered <- req.SessionID
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
	}
	gw.mu.Unlock()

	tickDone := make(chan struct{})
	go func() {
		if _, err := r.Tick(context.Background()); err != nil {
			t.Errorf("Tick: %v", err)
		}
		close(tickDone)
	}()

	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case s := <-entered:
			seen[s] = true
		case <-deadline:
			t.Fatalf("timed out after 2s waiting for both due runs' sends to start concurrently — only "+
				"%d entered (%v); under the old sequential Tick, run 2's send is never even attempted "+
				"until run 1's Send call returns", len(seen), seen)
		}
	}
	close(release)

	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Tick never returned after release")
	}

	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want 2 distinct senders (one per run)", len(seen))
	}
}

// TestConcurrencyIsBoundedByMaxConcurrentRuns pins the OTHER half of the
// same fix: fanning out must still be BOUNDED. Three due runs over three
// disjoint pairs, MaxConcurrentRuns=2 — the third send must not start until
// one of the first two returns.
func TestConcurrencyIsBoundedByMaxConcurrentRuns(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	gw := newFakeGateway()
	phones := map[string]string{
		"sess-a": "628110000001", "sess-b": "628110000002",
		"sess-c": "628110000003", "sess-d": "628110000004",
		"sess-e": "628110000005", "sess-f": "628110000006",
	}
	for _, id := range []string{"sess-a", "sess-b", "sess-c", "sess-d", "sess-e", "sess-f"} {
		gw.seed(Participant{ID: id, Name: id, Phone: phones[id], Connected: true})
	}

	r := New(repo, gw, Options{
		Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second,
		MaxConcurrentRuns: 2,
	})
	tmpl := seedExampleTemplate(t, repo)

	for _, pair := range [][2]string{{"sess-a", "sess-b"}, {"sess-c", "sess-d"}, {"sess-e", "sess-f"}} {
		if _, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{pair[0], pair[1]},
		}); err != nil {
			t.Fatalf("Trigger %v: %v", pair, err)
		}
	}

	started := make(chan struct{}, 3)
	release := make(chan struct{})
	gw.mu.Lock()
	gw.sendHook = func(req SendRequest) {
		started <- struct{}{}
		<-release
	}
	gw.mu.Unlock()

	tickDone := make(chan struct{})
	go func() {
		if _, err := r.Tick(context.Background()); err != nil {
			t.Errorf("Tick: %v", err)
		}
		close(tickDone)
	}()

	// Exactly two sends must start promptly...
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of the first 2 sends started before the 2s deadline", i)
		}
	}
	// ...and the third must NOT start while both of the first two are still
	// blocked on release — that would mean concurrency is unbounded.
	select {
	case <-started:
		t.Fatal("a third send started before either of the first two returned — " +
			"concurrency is not bounded by MaxConcurrentRuns")
	case <-time.After(150 * time.Millisecond):
	}

	close(release) // let everything proceed now that the bound has been proven
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the third send never started after the first two were released")
	}

	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Tick never returned")
	}
}

// ---- failure paths ----

func TestARunFailsWithTheSessionNameWhenAParticipantDisconnectsMidRun(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (turn 1): %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount after turn 1 = %d, want 1", n)
	}

	gw.setConnected(run.SessionBID, false)
	clock.Advance(time.Duration(turns[1].DelaySeconds+1) * time.Second)
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (should fail the run): %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount after disconnect = %d, want still 1 (no further sends)", n)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != convo.RunFailed {
		t.Fatalf("State = %q, want %q", got.State, convo.RunFailed)
	}
	if !strings.Contains(got.Error, run.SessionBName) {
		t.Fatalf("Error = %q, want it to contain %q", got.Error, run.SessionBName)
	}

	remaining, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	for _, tu := range remaining {
		if tu.Seq == 1 {
			continue
		}
		if tu.State != convo.TurnSkipped {
			t.Fatalf("turn %d state = %q, want %q", tu.Seq, tu.State, convo.TurnSkipped)
		}
	}
}

func TestASendErrorFailsTheRunAndRecordsTheReason(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	gw.mu.Lock()
	gw.sendErr = errors.New("waxum: boom")
	gw.mu.Unlock()

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != convo.RunFailed {
		t.Fatalf("State = %q, want %q", got.State, convo.RunFailed)
	}
	if !strings.Contains(got.Error, "boom") {
		t.Fatalf("Error = %q, want it to contain the send error", got.Error)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].State != convo.TurnFailed {
		t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnFailed)
	}
}

// ---- cancel ----

func TestCancelStopsFurtherSendsAndSkipsThePendingTurns(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (turn 1): %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount after turn 1 = %d, want 1", n)
	}

	if err := r.Cancel(context.Background(), run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	clock.Advance(24 * time.Hour)
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick after cancel: %v", err)
	}
	if n := gw.sendCount(); n != 1 {
		t.Fatalf("sendCount after cancel+tick = %d, want still 1", n)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != convo.RunCancelled {
		t.Fatalf("State = %q, want %q", got.State, convo.RunCancelled)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	for _, tu := range turns {
		if tu.Seq == 1 {
			continue
		}
		if tu.State != convo.TurnSkipped {
			t.Fatalf("turn %d state = %q, want %q", tu.Seq, tu.State, convo.TurnSkipped)
		}
	}
}

// TestATurnSkippedByACancelBetweenPickAndClaimIsNeverSent is the runner-level
// regression test for the exact race ClaimTurn's state = 'pending' guard
// exists to close: a Cancel landing after processRun's NextPendingTurn read
// but before its claim must win — the turn must be skipped, never sent, and
// the run must land cancelled, not part-way through a send it should have
// refused. onParticipant fires the cancel deterministically at that precise
// point in processRun's ordering (see its doc comment) rather than relying
// on goroutine timing.
func TestATurnSkippedByACancelBetweenPickAndClaimIsNeverSent(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	gw.mu.Lock()
	gw.onParticipant = func() {
		if err := repo.CancelRun(context.Background(), run.ID, clock.Now()); err != nil {
			t.Errorf("CancelRun (from onParticipant hook): %v", err)
		}
	}
	gw.mu.Unlock()

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := gw.sendCount(); n != 0 {
		t.Fatalf("sendCount = %d, want 0 (the turn was cancelled out from under the claim and must never be sent)", n)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	for _, tu := range turns {
		if tu.State != convo.TurnSkipped {
			t.Fatalf("turn %d state = %q, want %q", tu.Seq, tu.State, convo.TurnSkipped)
		}
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != convo.RunCancelled {
		t.Fatalf("run state = %q, want %q", got.State, convo.RunCancelled)
	}
}

// ---- restart safety ----

func TestAFreshRunnerPicksUpADueRunLeftByAPreviousProcess(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo1 := convo.NewRepository(pool)
	gw1 := newFakeGateway()
	gw1.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw1.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner1 := New(repo1, gw1, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	tmpl := seedExampleTemplate(t, repo1)

	if _, err := runner1.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// runner1 never ticks — simulating a process that triggered a run and
	// then died before its own loop ever ran.

	repo2 := convo.NewRepository(pool)
	gw2 := newFakeGateway()
	gw2.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw2.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner2 := New(repo2, gw2, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})

	if _, err := runner2.Tick(context.Background()); err != nil {
		t.Fatalf("runner2.Tick: %v", err)
	}
	if n := gw2.sendCount(); n != 1 {
		t.Fatalf("runner2 sendCount = %d, want 1 (it should pick up the due run runner1 left behind)", n)
	}
}

func TestATurnFoundMidSendAfterARestartIsMarkedInterruptedAndTheRunContinues(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo1 := convo.NewRepository(pool)
	gw1 := newFakeGateway()
	gw1.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw1.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner1 := New(repo1, gw1, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	tmpl := seedExampleTemplate(t, repo1)

	run, err := runner1.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if _, err := runner1.Tick(context.Background()); err != nil {
		t.Fatalf("runner1.Tick (turn 1): %v", err)
	}
	if n := gw1.sendCount(); n != 1 {
		t.Fatalf("runner1 sendCount = %d, want 1", n)
	}

	// Simulate the OLD process crashing mid-send of turn 2: its state is
	// "sending" with no terminal write ever landing, and the run is left
	// due right now so the new process finds it immediately. ClaimTurn now
	// stamps message_id atomically with the state change (item 3 of the
	// runner review — see convo.Repository.ClaimTurn's doc comment), so
	// there is no separate SetTurnMessageID call to make here any more.
	if err := repo1.ClaimTurn(context.Background(), run.ID, 2, "stale-message-id"); err != nil {
		t.Fatalf("ClaimTurn: %v", err)
	}
	if err := repo1.AdvanceRun(context.Background(), run.ID, 1, clock.Now()); err != nil {
		t.Fatalf("AdvanceRun: %v", err)
	}

	repo2 := convo.NewRepository(pool)
	gw2 := newFakeGateway()
	gw2.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw2.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner2 := New(repo2, gw2, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})

	// Recover no longer runs implicitly inside Tick (it moved to a
	// once-at-boot call from Start — see runner.go's doc comment on both);
	// a test driving Tick directly, as this one does, must call it itself.
	// "stale-message-id" has no configured MessageStatus answer on gw2, so
	// it is reported not-found, and Recover falls back to "interrupted" —
	// the same verdict the old inline MarkInterrupted produced.
	if err := runner2.Recover(context.Background()); err != nil {
		t.Fatalf("runner2.Recover: %v", err)
	}
	if _, err := runner2.Tick(context.Background()); err != nil {
		t.Fatalf("runner2.Tick: %v", err)
	}
	if n := gw2.sendCount(); n != 1 {
		t.Fatalf("runner2 sendCount = %d, want 1 (turn 3, since turn 2 is no longer pending)", n)
	}

	turns, err := repo2.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[1].State != convo.TurnInterrupted {
		t.Fatalf("turn 2 state = %q, want %q", turns[1].State, convo.TurnInterrupted)
	}
	if turns[2].State != convo.TurnSent {
		t.Fatalf("turn 3 state = %q, want %q", turns[2].State, convo.TurnSent)
	}
}

// ---- Recover ----

// TestRecoverMarksACutOffTurnSentWhenTheAuditRowSaysItWentOut is the
// RED-first regression test for item 3 of the runner review: a turn found
// "sending" at boot whose wa_messages row says "sent" must be reconciled as
// sent, not blindly marked "interrupted" the way the old, blanket
// MarkInterrupted-in-processRun path always did.
func TestRecoverMarksACutOffTurnSentWhenTheAuditRowSaysItWentOut(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// Simulate a crash between ClaimTurn (which now stores the message id
	// before Send is ever called) and this process's own MarkTurnSent write
	// — the audit row landed, but the turn's own state didn't.
	if err := repo.ClaimTurn(context.Background(), run.ID, 1, "msg-crash-sent"); err != nil {
		t.Fatalf("ClaimTurn: %v", err)
	}
	gw.setMessageStatus("msg-crash-sent", MessageStatusSent, "")

	if err := r.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].State != convo.TurnSent {
		t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnSent)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.TurnsDone != 1 {
		t.Fatalf("TurnsDone = %d, want 1 (Recover must count a reconciled 'sent' turn as progress)", got.TurnsDone)
	}
	if got.NextDueAt == nil || !got.NextDueAt.Equal(clock.Now()) {
		t.Fatalf("NextDueAt = %v, want %v (now, so the next Tick picks the run back up immediately)",
			got.NextDueAt, clock.Now())
	}
}

// TestRecoverMarksACutOffTurnFailedWhenTheAuditRowSaysSo pins the "failed"
// branch, including that MarkTurnFailed is called with the row's OWN stored
// reason rather than a reason Recover invents.
func TestRecoverMarksACutOffTurnFailedWhenTheAuditRowSaysSo(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	if err := repo.ClaimTurn(context.Background(), run.ID, 1, "msg-crash-failed"); err != nil {
		t.Fatalf("ClaimTurn: %v", err)
	}
	gw.setMessageStatus("msg-crash-failed", MessageStatusFailed, "waxum: boom")

	if err := r.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].State != convo.TurnFailed {
		t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnFailed)
	}
	if turns[0].Error != "waxum: boom" {
		t.Fatalf("turn 1 Error = %q, want the audit row's own reason %q", turns[0].Error, "waxum: boom")
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.TurnsDone != 0 {
		t.Fatalf("TurnsDone = %d, want 0 (a failed turn is not progress)", got.TurnsDone)
	}
	if got.NextDueAt == nil || !got.NextDueAt.Equal(clock.Now()) {
		t.Fatalf("NextDueAt = %v, want %v (now, so the next Tick picks the run back up immediately)",
			got.NextDueAt, clock.Now())
	}
}

// TestRecoverLeavesATurnInterruptedWhenTheAuditRowIsStillQueuedOrMissing
// pins the fallback bucket: a status Recover cannot explain (still "queued",
// or no wa_messages row at all) must land as "interrupted" — the same
// honest "don't know" verdict the old blanket MarkInterrupted always gave,
// never a guessed sent/failed.
func TestRecoverLeavesATurnInterruptedWhenTheAuditRowIsStillQueuedOrMissing(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
		r, repo, gw := newTestRunner(t, clock)
		tmpl := seedExampleTemplate(t, repo)
		run, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
		})
		if err != nil {
			t.Fatalf("Trigger: %v", err)
		}
		if err := repo.ClaimTurn(context.Background(), run.ID, 1, "msg-still-queued"); err != nil {
			t.Fatalf("ClaimTurn: %v", err)
		}
		gw.setMessageStatus("msg-still-queued", MessageStatusQueued, "")

		if err := r.Recover(context.Background()); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		turns, err := repo.Turns(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("Turns: %v", err)
		}
		if turns[0].State != convo.TurnInterrupted {
			t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnInterrupted)
		}
	})

	t.Run("no wa_messages row at all", func(t *testing.T) {
		clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
		r, repo, _ := newTestRunner(t, clock)
		tmpl := seedExampleTemplate(t, repo)
		run, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
		})
		if err != nil {
			t.Fatalf("Trigger: %v", err)
		}
		// No setMessageStatus call — the fake gateway's default is found=false.
		if err := repo.ClaimTurn(context.Background(), run.ID, 1, "msg-never-existed"); err != nil {
			t.Fatalf("ClaimTurn: %v", err)
		}

		if err := r.Recover(context.Background()); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		turns, err := repo.Turns(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("Turns: %v", err)
		}
		if turns[0].State != convo.TurnInterrupted {
			t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnInterrupted)
		}
	})
}

// ---- sleep computation ----

func TestTickSleepsUntilTheEarliestDueRunCappedByThePollInterval(t *testing.T) {
	const poll = 5 * time.Second
	baseClock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	t.Run("no runs at all", func(t *testing.T) {
		clock := newFakeClock(baseClock)
		r, _, _ := newTestRunner(t, clock)
		r.poll = poll
		sleep, err := r.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if sleep != poll {
			t.Fatalf("sleep = %v, want %v (poll interval, nothing scheduled)", sleep, poll)
		}
	})

	t.Run("a run due soon, under the poll interval", func(t *testing.T) {
		clock := newFakeClock(baseClock)
		r, repo, _ := newTestRunner(t, clock)
		r.poll = poll
		tmpl := seedExampleTemplate(t, repo)
		if _, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
			StartAt: clock.Now().Add(2 * time.Second),
		}); err != nil {
			t.Fatalf("Trigger: %v", err)
		}
		sleep, err := r.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if sleep != 2*time.Second {
			t.Fatalf("sleep = %v, want 2s", sleep)
		}
	})

	t.Run("a run due far in the future is capped by the poll interval", func(t *testing.T) {
		clock := newFakeClock(baseClock)
		r, repo, _ := newTestRunner(t, clock)
		r.poll = poll
		tmpl := seedExampleTemplate(t, repo)
		if _, err := r.Trigger(context.Background(), TriggerRequest{
			TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
			StartAt: clock.Now().Add(10 * time.Minute),
		}); err != nil {
			t.Fatalf("Trigger: %v", err)
		}
		sleep, err := r.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if sleep != poll {
			t.Fatalf("sleep = %v, want %v (capped)", sleep, poll)
		}
	})
}

// newTestRunnerWithPool is newTestRunner, but also hands back the pool
// itself — needed by the two tests below, which flip the connection into
// read-only mode (PRAGMA query_only) to force every write processRun makes
// to fail while every read keeps succeeding, the cheapest deterministic way
// to reproduce "a due run's advance fails" without a fake repo. Open pins
// MaxOpenConns(1) (internal/db.Open's doc comment), so one connection's
// pragma applies to every query the pool ever issues.
func newTestRunnerWithPool(t *testing.T, clock *fakeClock) (*Runner, *convo.Repository, *fakeGateway, *sql.DB) {
	t.Helper()
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	gw := newFakeGateway()
	gw.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	r := New(repo, gw, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	return r, repo, gw, pool
}

// makePoolReadOnly forces every subsequent write through pool to fail with
// "attempt to write a readonly database" while SELECTs keep succeeding —
// verified directly against modernc.org/sqlite before relying on it here.
func makePoolReadOnly(t *testing.T, pool *sql.DB) {
	t.Helper()
	if _, err := pool.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatalf("PRAGMA query_only = ON: %v", err)
	}
}

func TestTickNeverReturnsAZeroSleepWhenADueRunCannotBeAdvanced(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _, pool := newTestRunnerWithPool(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// The run is due right now (StartAt defaulted to clock.Now()). Force
	// every write processRun attempts (SetRunning, ClaimTurn, ...) to
	// fail, while DueRuns/EarliestDue — plain SELECTs — keep succeeding, so
	// EarliestDue still reports the run's now-stale, already-past
	// next_due_at exactly as it would during a real MySQL read-only
	// failover.
	makePoolReadOnly(t, pool)

	sleep, err := r.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sleep <= 0 {
		t.Fatalf("sleep = %v, want a positive floor — a zero sleep here spins loop() at CPU speed "+
			"against a due run that can never be advanced (the run's own next_due_at never moved: %v)",
			sleep, mustGetNextDue(t, repo, run.ID))
	}
}

func mustGetNextDue(t *testing.T, repo *convo.Repository, runID string) *time.Time {
	t.Helper()
	got, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return got.NextDueAt
}

func TestTickBacksOffAcrossConsecutiveFailingTicksAndResetsOnACleanOne(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _, pool := newTestRunnerWithPool(t, clock)
	r.poll = 5 * time.Second
	tmpl := seedExampleTemplate(t, repo)

	if _, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	makePoolReadOnly(t, pool)

	wantFloors := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	for i, want := range wantFloors {
		sleep, err := r.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick #%d: %v", i+1, err)
		}
		if sleep != want {
			t.Fatalf("Tick #%d sleep = %v, want %v (1s -> 2s -> 4s exponential backoff across "+
				"consecutive erroring ticks, capped by the 5s poll interval)", i+1, sleep, want)
		}
	}

	// A tick that logs no processRun error resets the backoff — restoring
	// write access lets the very next tick advance the run normally, and
	// nothing about that tick's sleep should still carry the 4s backoff.
	if _, err := pool.Exec(`PRAGMA query_only = OFF`); err != nil {
		t.Fatalf("PRAGMA query_only = OFF: %v", err)
	}
	sleep, err := r.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick (clean pass): %v", err)
	}
	if sleep == 4*time.Second {
		t.Fatalf("sleep = %v, backoff did not reset after a clean tick", sleep)
	}
}

// ---- Start/Stop ----

func TestStopJoinsTheLoopAndAnInFlightTickReturnsOnCancel(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	gw.mu.Lock()
	gw.blockOnCtx = true
	gw.mu.Unlock()

	r.Start(context.Background())

	deadline := time.After(2 * time.Second)
	for gw.sendCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("Send was never called before the deadline")
		case <-time.After(time.Millisecond):
		}
	}

	stopped := make(chan struct{})
	start := time.Now()
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s of an in-flight, ctx-blocked Send")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop took %v, want well under 1s", elapsed)
	}

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	// A graceful Stop must leave a cut-off send EXACTLY where a crash would
	// — "sending", never "failed" and never "sent" — because the send may
	// still be running against its own transport deadline and its outcome
	// is genuinely unknown here. Marking it failed would be WORSE than a
	// crash: the next boot's MarkInterrupted turns "sending" into
	// "interrupted" and resumes the run with the next turn (processRun's
	// doc comment); a "failed" verdict recorded here would instead
	// conclude the run outright on a guess.
	if turns[0].State != convo.TurnSending {
		t.Fatalf("turn 1 state = %q, want %q (a graceful stop must leave a cut-off send exactly like a crash would)", turns[0].State, convo.TurnSending)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if convo.IsTerminal(got.State) {
		t.Fatalf("run state = %q, want a non-terminal state — graceful shutdown must not conclude the run; the next boot resumes it", got.State)
	}
}

// TestAGracefulStopLeavesAnInFlightTurnSendingSoTheNextBootResumesTheRun
// covers the recovery half of the story the previous test only sets up:
// Stop mid-send leaves the cut-off turn "sending"; a FRESH Runner (the next
// boot, over the same pool) must find it via MarkInterrupted and continue
// sending the rest of the run — the same path
// TestATurnFoundMidSendAfterARestartIsMarkedInterruptedAndTheRunContinues
// already proves for a hard crash. This is the "resumes exactly like a
// crash" half of processRun's doc comment.
func TestAGracefulStopLeavesAnInFlightTurnSendingSoTheNextBootResumesTheRun(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo1 := convo.NewRepository(pool)
	gw1 := newFakeGateway()
	gw1.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw1.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner1 := New(repo1, gw1, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})
	tmpl := seedExampleTemplate(t, repo1)

	run, err := runner1.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	gw1.mu.Lock()
	gw1.blockOnCtx = true
	gw1.mu.Unlock()

	runner1.Start(context.Background())
	deadline := time.After(2 * time.Second)
	for gw1.sendCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("Send was never called before the deadline")
		case <-time.After(time.Millisecond):
		}
	}
	runner1.Stop()

	turns, err := repo1.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].State != convo.TurnSending {
		t.Fatalf("turn 1 state after Stop = %q, want %q", turns[0].State, convo.TurnSending)
	}

	// The next boot: a fresh Runner over the same pool, with a gateway that
	// actually completes sends (no blockOnCtx).
	repo2 := convo.NewRepository(pool)
	gw2 := newFakeGateway()
	gw2.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw2.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	runner2 := New(repo2, gw2, Options{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second})

	// Recover no longer runs implicitly inside Tick (see runner.go's doc
	// comment) — a test driving Tick directly, as this one does, must call
	// it itself. turn 1's message_id (minted by runner1's own ClaimTurn,
	// which now stamps it BEFORE Send is even called) has no configured
	// MessageStatus answer on gw2 — a fresh gateway instance — so it is
	// reported not-found and Recover falls back to "interrupted".
	if err := runner2.Recover(context.Background()); err != nil {
		t.Fatalf("runner2.Recover: %v", err)
	}
	if _, err := runner2.Tick(context.Background()); err != nil {
		t.Fatalf("runner2.Tick: %v", err)
	}
	if n := gw2.sendCount(); n != 1 {
		t.Fatalf("runner2 sendCount = %d, want 1 (turn 2, since turn 1's cut-off send is never re-sent)", n)
	}

	turns2, err := repo2.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns2[0].State != convo.TurnInterrupted {
		t.Fatalf("turn 1 state after reboot = %q, want %q", turns2[0].State, convo.TurnInterrupted)
	}
	if turns2[1].State != convo.TurnSent {
		t.Fatalf("turn 2 state after reboot = %q, want %q (the run must resume with the next turn)", turns2[1].State, convo.TurnSent)
	}

	got, err := repo2.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if convo.IsTerminal(got.State) {
		t.Fatalf("run state after reboot = %q, want non-terminal (still in progress)", got.State)
	}
}

// TestCancellingTheParentContextDoesNotStopTheRunner proves Start's
// context.WithoutCancel derivation directly: cancelling the ctx passed TO
// Start must not stop the loop — only Stop() may. Without the fix this
// test is RED because the loop's ctx dies the instant parentCancel() runs,
// so the due run below is never picked up.
func TestCancellingTheParentContextDoesNotStopTheRunner(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)
	t.Cleanup(r.Stop)

	parentCtx, parentCancel := context.WithCancel(context.Background())
	r.Start(parentCtx)
	parentCancel() // simulate main.go's SIGTERM ctx ending before Stop() runs

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// Only the first turn is expected within the deadline — later turns
	// wait on the script's own pacing delay, which is irrelevant here; what
	// matters is that the loop processed the due run AT ALL after its
	// parent ctx ended, which it could never do if Start had derived the
	// loop from a cancellable ctx.
	deadline := time.After(2 * time.Second)
	for gw.sendCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("the runner never processed the due run after its parent ctx was cancelled — Start must not derive the loop from a cancellable ctx")
		case <-time.After(time.Millisecond):
		}
	}

	r.Stop()

	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].State != convo.TurnSent {
		t.Fatalf("turn 1 state = %q, want %q", turns[0].State, convo.TurnSent)
	}
	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != convo.RunRunning {
		t.Fatalf("run state = %q, want %q (still in progress, not stuck and not finished early)", got.State, convo.RunRunning)
	}
}
