// chain.go: turning one finished run into its follow-up conversation. A
// schedule with ChainEnabled links templates into a directed, WEIGHTED graph
// (convo.TemplateLink) — "after this template, these are plausible next
// conversations, and here is how likely each one is and how long to wait
// before starting it". NextTemplate draws the next hop; PickOpener draws the
// very first template of a topic when a schedule pins no root_template_id.
// See runner.go's continueChain for how a finished run's successor is
// actually scheduled from these two draws.
package selfconvo

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// ErrNoTemplatesForTopic is returned by PickOpener when topic has no
// opener-stage templates at all — an operator-fixable corpus/config gap
// (a topic with nothing to open a conversation with), named explicitly so
// it reads as "fix the corpus for this topic" rather than an opaque
// not-found.
var ErrNoTemplatesForTopic = errors.New("selfconvo: no opener templates for topic")

// NextTemplate draws the successor of fromTemplateID from links — the
// FromTemplateID's own outgoing edges, weighted by TemplateLink.Weight — and
// draws a gap uniformly from that edge's [MinGapSeconds, MaxGapSeconds].
// Returns ok=false when links is empty or every edge's weight is <= 0: a
// template with no reachable outgoing edge is a leaf, and a chain that
// reaches one ends there rather than restarting arbitrarily (see
// runner.go's continueChain, which treats ok=false as "the chain ends here,
// quietly, not an error").
//
// The draw is a single rng.IntN(total) against the CUMULATIVE weight, which
// is what keeps it uniform-correct: an edge of weight 3 among edges
// totalling weight 10 owns exactly 3 of those 10 outcomes, no matter how
// many other edges surround it or in what order links is given.
func NextTemplate(links []convo.TemplateLink, rng *rand.Rand) (id string, gap time.Duration, ok bool) {
	total := 0
	for _, l := range links {
		if l.Weight > 0 {
			total += l.Weight
		}
	}
	if total <= 0 {
		return "", 0, false
	}

	draw := rng.IntN(total)
	cum := 0
	for _, l := range links {
		if l.Weight <= 0 {
			continue
		}
		cum += l.Weight
		if draw < cum {
			return l.ToTemplateID, drawGap(rng, l.MinGapSeconds, l.MaxGapSeconds), true
		}
	}
	// Unreachable: draw is always < total, and cum reaches total on the last
	// eligible edge. Kept as a safety net rather than a panic.
	return "", 0, false
}

// drawGap draws a uniform integer number of seconds in [min, max] inclusive
// — the same shape as expand.go's drawDelay, tolerating a malformed
// (max < min, or both <= 0) window by falling back to min (clamped to 0)
// rather than panicking on a negative rng.IntN argument.
func drawGap(rng *rand.Rand, min, max int) time.Duration {
	if min < 0 {
		min = 0
	}
	span := max - min
	if span <= 0 {
		return time.Duration(min) * time.Second
	}
	return time.Duration(min+rng.IntN(span+1)) * time.Second
}

// PickOpener draws a template id uniformly from topic's opener-stage
// templates (convo.StageOpener) — the entry point a schedule uses when it
// pins no root_template_id of its own. Returns ErrNoTemplatesForTopic,
// naming topic, when there are none: an empty corpus for a topic is an
// operator/corpus problem, not an internal fault, and the error says so by
// name rather than reading like one.
func PickOpener(ctx context.Context, repo *convo.Repository, topic string, rng *rand.Rand) (string, error) {
	ids, err := repo.TemplateIDsByTopicStage(ctx, topic, convo.StageOpener, 0)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: %q", ErrNoTemplatesForTopic, topic)
	}
	return ids[rng.IntN(len(ids))], nil
}
