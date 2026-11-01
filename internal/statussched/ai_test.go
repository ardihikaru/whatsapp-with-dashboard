package statussched

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
)

// fakeAI records every prompt and answers from a script. block makes it
// hang until the caller's context ends, like a stuck ai-codex-cli job.
type fakeAI struct {
	prompts []string
	replies []string
	err     error
	block   bool
	onCall  func()
}

func (f *fakeAI) Generate(ctx context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if f.onCall != nil {
		f.onCall()
	}
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if f.err != nil {
		return "", f.err
	}
	i := len(f.prompts) - 1
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	return f.replies[i], nil
}

// newAIHarness is newHarness with a generator wired into the Scheduler.
func newAIHarness(t *testing.T, now time.Time, ai TextGenerator) *harness {
	t.Helper()
	h := newHarness(t, now)
	h.s = New(h.repo, h.gw, Options{Now: func() time.Time { return h.now }, Rand: rand.New(rand.NewPCG(7, 9)),
		AI: ai, AIDeadline: 50 * time.Millisecond})
	return h
}

func aiSchedule(mut func(*sched.Schedule)) func(*sched.Schedule) {
	return func(s *sched.Schedule) {
		s.AIEnabled, s.AITopic = true, "kopi pagi yang hangat"
		if mut != nil {
			mut(s)
		}
	}
}

// Monday 2026-11-02 06:00 WIB: the 07:00-10:27 window is entirely ahead.
func mondayEarly() time.Time { return utc(2026, 11, 1, 23, 0) }

func TestAnAIScheduleGeneratesOncePerPlannedSlotAndTheFirePostsThatText(t *testing.T) {
	ai := &fakeAI{replies: []string{"Selamat pagi, ngopi dulu yuk!"}}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(func(s *sched.Schedule) { s.Text = "static fallback" }))

	h.tick()
	h.tick() // a second tick the same day must not re-plan or re-generate
	if len(ai.prompts) != 1 {
		t.Fatalf("AI calls after planning = %d, want exactly 1", len(ai.prompts))
	}
	sc := h.get("a")
	if sc.NextText != "Selamat pagi, ngopi dulu yuk!" || sc.NextTextSource != sched.TextSourceAI || sc.LastAIText != sc.NextText {
		t.Fatalf("plan did not store the AI text: %+v", sc)
	}

	h.now = sc.NextFireAt.Add(time.Second)
	h.tick()
	h.tick()
	if len(h.gw.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(h.gw.posts))
	}
	if got := h.gw.posts[0].Schedule.Text; got != "Selamat pagi, ngopi dulu yuk!" {
		t.Errorf("posted text = %q, want the planned AI text (not the static fallback)", got)
	}
	if len(ai.prompts) != 1 {
		t.Errorf("AI calls after firing = %d, want still 1: the fire path must never call the model", len(ai.prompts))
	}
	if got := h.get("a"); got.LastResult != sched.ResultSent {
		t.Errorf("LastResult = %q, want sent", got.LastResult)
	}
}

func TestAnAIImageScheduleWritesTheCaptionAndLeavesTheTextAlone(t *testing.T) {
	ai := &fakeAI{replies: []string{"Promo hari ini!"}}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(func(s *sched.Schedule) {
		s.Kind, s.Text, s.Caption, s.MediaObjectKey = sched.KindImage, "", "", "k"
	}))
	h.tick()
	h.now = h.get("a").NextFireAt
	h.tick()
	if len(h.gw.posts) != 1 {
		t.Fatalf("posts = %d, want 1 (an empty static caption is not a reason to skip when the AI succeeded)", len(h.gw.posts))
	}
	p := h.gw.posts[0].Schedule
	if p.Caption != "Promo hari ini!" || p.Text != "" {
		t.Errorf("caption=%q text=%q, want the AI text as caption only", p.Caption, p.Text)
	}
	if !strings.Contains(ai.prompts[0], "caption") {
		t.Errorf("an image schedule's prompt should ask for a caption: %q", ai.prompts[0])
	}
}

func TestThePromptCarriesTheTopicTheLastTextAndTheOnlyTheFinalTextInstruction(t *testing.T) {
	ai := &fakeAI{replies: []string{"Hari pertama", "Hari ketiga"}}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(nil))
	h.tick()
	first := ai.prompts[0]
	for _, want := range []string{"kopi pagi yang hangat", "SAME LANGUAGE", "ONLY the final message text", "no quotes"} {
		if !strings.Contains(first, want) {
			t.Errorf("first prompt lacks %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "Do not repeat") {
		t.Errorf("first prompt mentions a previous text that does not exist:\n%s", first)
	}

	// Fire Monday, then plan Wednesday: the second prompt must quote Monday's text.
	h.now = h.get("a").NextFireAt
	h.tick()
	h.now = utc(2026, 11, 3, 23, 0) // Wed 06:00 WIB
	h.tick()
	if len(ai.prompts) != 2 {
		t.Fatalf("AI calls = %d, want 2 (one per planned day)", len(ai.prompts))
	}
	second := ai.prompts[1]
	for _, want := range []string{"kopi pagi yang hangat", "Hari pertama", "Do not repeat or closely paraphrase", "ONLY the final message text"} {
		if !strings.Contains(second, want) {
			t.Errorf("second prompt lacks %q:\n%s", want, second)
		}
	}
}

func TestAFailedGenerationFallsBackToTheStaticTextAndKeepsTheLastRealAIText(t *testing.T) {
	ai := &fakeAI{err: errors.New("codex down")}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(func(s *sched.Schedule) { s.Text, s.LastAIText = "static fallback", "older ai text" }))
	h.tick()
	sc := h.get("a")
	if sc.NextText != "static fallback" || sc.NextTextSource != sched.TextSourceStatic || sc.LastAIText != "older ai text" {
		t.Fatalf("fallback plan wrong: %+v", sc)
	}
	h.now = sc.NextFireAt
	h.tick()
	if len(h.gw.posts) != 1 || h.gw.posts[0].Schedule.Text != "static fallback" {
		t.Fatalf("posts = %+v, want one post with the static fallback", h.gw.posts)
	}
}

func TestAFailedGenerationWithNoFallbackSkipsTheDayAndRecordsWhy(t *testing.T) {
	ai := &fakeAI{err: errors.New("codex down")}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(nil)) // Text = "hello" from add(); clear it
	sc := h.get("a")
	sc.Text = ""
	if err := h.repo.Update(h.ctx, &sc); err != nil {
		t.Fatal(err)
	}
	h.tick()
	got := h.get("a")
	if !got.NextFireAt.IsZero() || got.LastPlannedDate != "2026-11-02" {
		t.Errorf("a skipped day must be marked planned with no slot: %+v", got)
	}
	if got.LastResult != sched.ResultSkipped || !strings.Contains(got.LastError, "AI generation failed: codex down") {
		t.Errorf("last result/error = %q / %q, want skipped + 'AI generation failed: codex down'", got.LastResult, got.LastError)
	}
	h.now = h.now.Add(2 * time.Hour)
	h.tick()
	h.tick()
	if len(h.gw.posts) != 0 {
		t.Fatalf("posted %d times after a failed generation with no fallback; must never post empty text", len(h.gw.posts))
	}
	if len(ai.prompts) != 1 {
		t.Errorf("AI calls = %d, want 1: a skipped day is not retried", len(ai.prompts))
	}
}

func TestWithoutAnAIClientTheFallbackOrASkipAppliesAndTheErrorSaysNotConfigured(t *testing.T) {
	// nil interface, as cmd/app passes when WSM_CODEX_CLI_* is absent.
	h := newAIHarness(t, mondayEarly(), nil)
	h.add("with", aiSchedule(func(s *sched.Schedule) { s.Text = "static fallback" }))
	h.add("without", aiSchedule(func(s *sched.Schedule) { s.Text = "" }))
	h.tick()
	with := h.get("with")
	if with.NextText != "static fallback" || with.NextTextSource != sched.TextSourceStatic {
		t.Errorf("nil AI + fallback: %+v", with)
	}
	without := h.get("without")
	if !without.NextFireAt.IsZero() || without.LastResult != sched.ResultSkipped ||
		!strings.Contains(without.LastError, "AI is not configured") {
		t.Errorf("nil AI, no fallback: next=%v result=%q error=%q", without.NextFireAt, without.LastResult, without.LastError)
	}
}

func TestAHungGenerationIsCutOffAtTheDeadlineAndNeverDelaysADuePost(t *testing.T) {
	ai := &fakeAI{block: true}
	h := newAIHarness(t, mondayEarly(), ai)
	// A plain schedule already due must be posted BEFORE the AI schedule's
	// planning starts blocking.
	h.add("plain", func(s *sched.Schedule) {
		s.LastPlannedDate = "2026-11-02"
		s.NextFireAt = utc(2026, 11, 1, 23, 0)
	})
	h.add("ai", aiSchedule(func(s *sched.Schedule) { s.Text = "static fallback" }))
	var postsWhenGenerationStarted int
	ai.onCall = func() { postsWhenGenerationStarted = len(h.gw.posts) }

	start := time.Now()
	h.tick()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Tick took %s with a hung generator; the 50ms deadline did not bound it", took)
	}
	if postsWhenGenerationStarted != 1 {
		t.Errorf("posts when the AI call began = %d, want 1: due posts must fire before slow generation", postsWhenGenerationStarted)
	}
	if got := h.get("ai"); got.NextTextSource != sched.TextSourceStatic || got.NextText != "static fallback" {
		t.Errorf("a timed-out generation must fall back to the static text: %+v", got)
	}
}

func TestNoGenerationHappensWhenTheWindowIsAlreadyOver(t *testing.T) {
	ai := &fakeAI{replies: []string{"x"}}
	h := newAIHarness(t, utc(2026, 11, 2, 5, 0), ai) // 12:00 WIB, window ended 10:27
	h.add("a", aiSchedule(nil))
	h.tick()
	if len(ai.prompts) != 0 {
		t.Errorf("AI was called %d times for a day that is skipped anyway", len(ai.prompts))
	}
}

func TestAnAIPlanWithNoStoredWordingIsSkippedNotPostedEmpty(t *testing.T) {
	ai := &fakeAI{replies: []string{"x"}}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", aiSchedule(func(s *sched.Schedule) {
		s.Text = ""
		s.LastPlannedDate = "2026-11-02"
		s.NextFireAt = utc(2026, 11, 1, 23, 30) // planned (e.g. by an older binary) with no next_text
	}))
	h.now = utc(2026, 11, 1, 23, 31)
	h.tick()
	if len(h.gw.posts) != 0 {
		t.Fatal("posted an AI schedule that has no planned wording")
	}
	if got := h.get("a"); got.LastResult != sched.ResultSkipped {
		t.Errorf("LastResult = %q, want skipped", got.LastResult)
	}
	if len(ai.prompts) != 0 {
		t.Error("the fire path called the AI")
	}
}

func TestAScheduleWithAIOffNeverTouchesTheGeneratorAndPostsItsOwnText(t *testing.T) {
	ai := &fakeAI{replies: []string{"nope"}}
	h := newAIHarness(t, mondayEarly(), ai)
	h.add("a", nil)
	h.tick()
	h.now = h.get("a").NextFireAt
	h.tick()
	if len(ai.prompts) != 0 || len(h.gw.posts) != 1 || h.gw.posts[0].Schedule.Text != "hello" {
		t.Errorf("AI off: prompts=%d posts=%+v", len(ai.prompts), h.gw.posts)
	}
}
