package selfconvo

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// TestNextTemplateReturnsNotOkForALeafWithNoOutgoingEdges pins the "a chain
// that reaches a leaf ends there" contract for the simplest possible leaf:
// no edges at all. Must not panic (an empty edge set is the normal shape of
// a closer template, not malformed input).
func TestNextTemplateReturnsNotOkForALeafWithNoOutgoingEdges(t *testing.T) {
	_, _, ok := NextTemplate(nil, rand.New(rand.NewPCG(1, 2)))
	if ok {
		t.Fatal("NextTemplate(nil, ...) ok = true, want false")
	}
}

// TestNextTemplateReturnsNotOkWhenEveryEdgeHasZeroWeight pins the other
// "must not divide by zero" shape: edges exist but none of them are
// reachable by weight — same outcome as no edges at all, never a panic.
func TestNextTemplateReturnsNotOkWhenEveryEdgeHasZeroWeight(t *testing.T) {
	links := []convo.TemplateLink{
		{ToTemplateID: "a", Weight: 0},
		{ToTemplateID: "b", Weight: 0},
	}
	_, _, ok := NextTemplate(links, rand.New(rand.NewPCG(1, 2)))
	if ok {
		t.Fatal("NextTemplate with every edge weight=0: ok = true, want false")
	}
}

// TestNextTemplateWeightedDistributionFavorsHigherWeight is the
// distribution test the phase brief asks for: over many draws, an edge of
// weight 3 must be chosen roughly three times as often as an edge of
// weight 1 — a band, not an exact count, since this is a random draw.
func TestNextTemplateWeightedDistributionFavorsHigherWeight(t *testing.T) {
	links := []convo.TemplateLink{
		{ToTemplateID: "heavy", Weight: 3, MinGapSeconds: 10, MaxGapSeconds: 10},
		{ToTemplateID: "light", Weight: 1, MinGapSeconds: 10, MaxGapSeconds: 10},
	}
	rng := rand.New(rand.NewPCG(7, 11))
	const draws = 20000
	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		id, _, ok := NextTemplate(links, rng)
		if !ok {
			t.Fatalf("draw %d: ok = false, want true (both edges have positive weight)", i)
		}
		counts[id]++
	}

	ratio := float64(counts["heavy"]) / float64(counts["light"])
	t.Logf("measured counts: heavy=%d light=%d ratio=%.3f (want close to 3.0)", counts["heavy"], counts["light"], ratio)
	// A generous band — this is a statistical property, not an exact
	// count — but 2.7..3.3 is tight enough to catch "picks uniformly
	// among edges" (ratio would be ~1.0) or "picks the last edge with
	// draw < cumulative-weight off by one" style bugs, while never
	// flaking at 20000 draws.
	if ratio < 2.7 || ratio > 3.3 {
		t.Fatalf("heavy/light ratio = %.3f, want roughly 3.0 (heavy=%d light=%d)", ratio, counts["heavy"], counts["light"])
	}
}

// TestNextTemplateDrawsGapWithinTheEdgesWindow pins the gap draw: always
// inside [MinGapSeconds, MaxGapSeconds] of the edge actually chosen,
// inclusive of both ends, across many draws.
func TestNextTemplateDrawsGapWithinTheEdgesWindow(t *testing.T) {
	links := []convo.TemplateLink{
		{ToTemplateID: "next", Weight: 1, MinGapSeconds: 60, MaxGapSeconds: 300},
	}
	rng := rand.New(rand.NewPCG(3, 5))
	for i := 0; i < 500; i++ {
		id, gap, ok := NextTemplate(links, rng)
		if !ok {
			t.Fatalf("draw %d: ok = false, want true", i)
		}
		if id != "next" {
			t.Fatalf("draw %d: id = %q, want %q", i, id, "next")
		}
		if gap < 60*time.Second || gap > 300*time.Second {
			t.Fatalf("draw %d: gap = %v, want within [60s, 300s]", i, gap)
		}
	}
}

// TestPickOpenerNamesTheTopicInItsError pins ErrNoTemplatesForTopic's own
// doc comment: an empty corpus for a topic is an operator-fixable problem
// and the error must name the topic, not read as an opaque not-found.
func TestPickOpenerNamesTheTopicInItsError(t *testing.T) {
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)

	_, err := PickOpener(context.Background(), repo, "futsal", rand.New(rand.NewPCG(1, 2)))
	if !errors.Is(err, ErrNoTemplatesForTopic) {
		t.Fatalf("err = %v, want ErrNoTemplatesForTopic", err)
	}
	if !strings.Contains(err.Error(), "futsal") {
		t.Fatalf("err = %q, want it to name the topic %q", err.Error(), "futsal")
	}
}

// TestPickOpenerDrawsOnlyFromTheOpenerStageOfTheGivenTopic proves PickOpener
// does not accidentally pick a middle/closer template, nor one from a
// different topic.
func TestPickOpenerDrawsOnlyFromTheOpenerStageOfTheGivenTopic(t *testing.T) {
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	ctx := context.Background()

	seed := func(id, topic, stage string) {
		tmpl := convo.Template{ID: id, Name: id, Topic: topic, Stage: stage, TurnCount: 2, Body: "{}"}
		if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
			t.Fatalf("InsertTemplate(%s): %v", id, err)
		}
	}
	seed("futsal-opener-1", "futsal", convo.StageOpener)
	seed("futsal-opener-2", "futsal", convo.StageOpener)
	seed("futsal-middle-1", "futsal", convo.StageMiddle)
	seed("other-opener-1", "arisan", convo.StageOpener)

	rng := rand.New(rand.NewPCG(9, 13))
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := PickOpener(ctx, repo, "futsal", rng)
		if err != nil {
			t.Fatalf("draw %d: PickOpener: %v", i, err)
		}
		if id != "futsal-opener-1" && id != "futsal-opener-2" {
			t.Fatalf("draw %d: PickOpener returned %q, want one of futsal's own opener templates", i, id)
		}
		seen[id] = true
	}
	if len(seen) != 2 {
		t.Fatalf("PickOpener over 50 draws only ever returned %v, want both opener templates represented", seen)
	}
}
