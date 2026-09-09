package selfconvo

import (
	"errors"
	"math/rand/v2"
)

// Participant is the slice of a paired WhatsApp session PickPair and Expand
// need. Phone is the session's OWN number, normalised to digits, so it can
// be used as the OTHER speaker's recipient address.
type Participant struct {
	ID        string
	Name      string
	Phone     string
	Connected bool
}

// Pair is the two participants a triggered run will play out between.
type Pair struct {
	A Participant
	B Participant
}

// ErrNotEnoughParticipants is returned by PickPair when fewer than two
// candidates are both connected and carry a phone number.
var ErrNotEnoughParticipants = errors.New("selfconvo: need at least two connected participants")

// PickPair filters candidates down to those that are Connected and have a
// non-empty Phone, DEDUPES them by ID (first occurrence wins — a session
// whose id was ticked twice, e.g. a form field submitted twice, must still
// count as ONE candidate, never as two independent draws that could both
// land as the same session), then draws two DISTINCT ones uniformly and
// assigns roles a/b at random — both (x as A, y as B) and (y as A, x as B)
// are reachable for the same candidate set, across seeds. Deterministic for
// a given rng: the same *rand.Rand state always produces the same pair and
// role assignment. Without the dedupe, candidates [x, x, y] could draw
// indices 0 and 1 — two DISTINCT indices, but the SAME participant twice —
// and pair a session with itself.
func PickPair(candidates []Participant, rng *rand.Rand) (Pair, error) {
	seen := make(map[string]bool, len(candidates))
	eligible := make([]Participant, 0, len(candidates))
	for _, c := range candidates {
		if c.Connected && c.Phone != "" && !seen[c.ID] {
			seen[c.ID] = true
			eligible = append(eligible, c)
		}
	}
	if len(eligible) < 2 {
		return Pair{}, ErrNotEnoughParticipants
	}

	// rng.Perm produces a uniformly random permutation of [0, len(eligible)),
	// so its first two entries are already a uniformly random ordered pair
	// of distinct indices — that gives PickPair both "which two" and "which
	// one plays A" in one draw, with no separate coin flip needed.
	order := rng.Perm(len(eligible))
	return Pair{A: eligible[order[0]], B: eligible[order[1]]}, nil
}

// ExpandedTurn is one scripted line resolved to a concrete sender, a
// concrete recipient, and a drawn (or overridden) delay before it is sent.
type ExpandedTurn struct {
	Seq          int // 1-based
	Speaker      string
	SessionID    string
	ToPhone      string
	Body         string
	DelaySeconds int
}

// Expand resolves every turn of an already-validated Script (Validate must
// have run first, so Script.Pacing is non-nil and every turn's Speaker is
// normalised to "a"/"b") against a chosen Pair: speaker "a" sends from
// pair.A to pair.B's phone and vice versa. A turn's DelaySeconds is its own
// override when set, otherwise a uniform draw from the script's pacing
// window — except the very first turn, whose delay is always forced to 0,
// since the run starts at start_at and has nothing to wait for yet.
func Expand(s Script, pair Pair, rng *rand.Rand) []ExpandedTurn {
	out := make([]ExpandedTurn, 0, len(s.Turns))
	for i, t := range s.Turns {
		sessionID, toPhone := pair.A.ID, pair.B.Phone
		if t.Speaker == "b" {
			sessionID, toPhone = pair.B.ID, pair.A.Phone
		}

		var delay int
		switch {
		case i == 0:
			delay = 0
		case t.DelaySeconds != nil:
			delay = *t.DelaySeconds
		default:
			delay = drawDelay(rng, s.Pacing)
		}

		out = append(out, ExpandedTurn{
			Seq:          i + 1,
			Speaker:      t.Speaker,
			SessionID:    sessionID,
			ToPhone:      toPhone,
			Body:         t.Text,
			DelaySeconds: delay,
		})
	}
	return out
}

// drawDelay draws a uniform integer in [p.MinSeconds, p.MaxSeconds],
// inclusive of both ends.
func drawDelay(rng *rand.Rand, p *Pacing) int {
	span := p.MaxSeconds - p.MinSeconds
	if span <= 0 {
		return p.MinSeconds
	}
	return p.MinSeconds + rng.IntN(span+1)
}
