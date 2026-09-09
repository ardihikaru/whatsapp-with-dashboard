package selfconvo

import (
	"math/rand/v2"
	"testing"
)

func TestPickPairChoosesTwoDistinctTickedSessionsAndAssignsBothRoleOrdersAcrossSeeds(t *testing.T) {
	x := Participant{ID: "x", Phone: "111", Connected: true}
	y := Participant{ID: "y", Phone: "222", Connected: true}
	z := Participant{ID: "z", Phone: "333", Connected: true}
	candidates := []Participant{x, y, z}

	seen := map[string]bool{}
	var sawXAsA, sawYAsA bool // proof both (x,y) and (y,x) orders occur

	for seed := uint64(0); seed < 50; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		pair, err := PickPair(candidates, rng)
		if err != nil {
			t.Fatalf("seed %d: PickPair returned error: %v", seed, err)
		}
		if pair.A.ID == pair.B.ID {
			t.Fatalf("seed %d: PickPair chose the same participant twice: %+v", seed, pair)
		}
		seen[pair.A.ID] = true
		seen[pair.B.ID] = true

		if pair.A.ID == "x" && pair.B.ID == "y" {
			sawXAsA = true
		}
		if pair.A.ID == "y" && pair.B.ID == "x" {
			sawYAsA = true
		}
	}

	for _, id := range []string{"x", "y", "z"} {
		if !seen[id] {
			t.Errorf("candidate %q never appeared in any pair across 50 seeds", id)
		}
	}
	if !sawXAsA {
		t.Errorf("never observed (x as A, y as B) across 50 seeds")
	}
	if !sawYAsA {
		t.Errorf("never observed (y as A, x as B) across 50 seeds")
	}
}

func TestPickPairRefusesFewerThanTwoConnectedCandidates(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))

	t.Run("two candidates but one disconnected", func(t *testing.T) {
		candidates := []Participant{
			{ID: "x", Phone: "111", Connected: true},
			{ID: "y", Phone: "222", Connected: false},
		}
		if _, err := PickPair(candidates, rng); err != ErrNotEnoughParticipants {
			t.Fatalf("expected ErrNotEnoughParticipants, got %v", err)
		}
	})

	t.Run("two connected candidates but one with an empty phone", func(t *testing.T) {
		candidates := []Participant{
			{ID: "x", Phone: "111", Connected: true},
			{ID: "y", Phone: "", Connected: true},
		}
		if _, err := PickPair(candidates, rng); err != ErrNotEnoughParticipants {
			t.Fatalf("expected ErrNotEnoughParticipants, got %v", err)
		}
	})
}

// TestPickPairNeverPairsASessionWithItselfWhenAnIdIsListedTwice is the
// regression test for a duplicate ticked session id: [x, x, y] must always
// draw {x, y}, never {x, x} — even though [0, 1] are two DISTINCT indices
// into the (undeduped) candidate slice, they would name the SAME
// participant twice. [x, x] alone (nothing else eligible) must fall back to
// ErrNotEnoughParticipants rather than "succeeding" with a self-pair.
func TestPickPairNeverPairsASessionWithItselfWhenAnIdIsListedTwice(t *testing.T) {
	x := Participant{ID: "x", Phone: "111", Connected: true}
	y := Participant{ID: "y", Phone: "222", Connected: true}
	candidates := []Participant{x, x, y}

	for seed := uint64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		pair, err := PickPair(candidates, rng)
		if err != nil {
			t.Fatalf("seed %d: PickPair([x, x, y]) returned error: %v", seed, err)
		}
		if pair.A.ID == pair.B.ID {
			t.Fatalf("seed %d: PickPair([x, x, y]) paired a session with itself: %+v", seed, pair)
		}
		ids := map[string]bool{pair.A.ID: true, pair.B.ID: true}
		if !ids["x"] || !ids["y"] {
			t.Fatalf("seed %d: PickPair([x, x, y]) = %+v, want exactly {x, y}", seed, pair)
		}
	}

	dupOnly := []Participant{x, x}
	if _, err := PickPair(dupOnly, rand.New(rand.NewPCG(1, 2))); err != ErrNotEnoughParticipants {
		t.Fatalf("PickPair([x, x]) = %v, want ErrNotEnoughParticipants", err)
	}
}

func validScriptForExpand(t *testing.T) Script {
	t.Helper()
	raw := `{
		"version": 1,
		"pacing": {"min_seconds": 10, "max_seconds": 20},
		"turns": [
			{"speaker": "a", "text": "Halo"},
			{"speaker": "b", "text": "Hai"},
			{"speaker": "a", "text": "Apa kabar?"}
		]
	}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}
	return s
}

func TestExpandResolvesEachSpeakerToItsSessionAndTheOtherSessionsPhone(t *testing.T) {
	s := validScriptForExpand(t)
	pair := Pair{
		A: Participant{ID: "session-a", Phone: "62811"},
		B: Participant{ID: "session-b", Phone: "62822"},
	}
	rng := rand.New(rand.NewPCG(1, 2))

	turns := Expand(s, pair, rng)
	if len(turns) != len(s.Turns) {
		t.Fatalf("expected %d expanded turns, got %d", len(s.Turns), len(turns))
	}

	for i, turn := range turns {
		if turn.Seq != i+1 {
			t.Errorf("turn %d: expected Seq %d, got %d", i, i+1, turn.Seq)
		}
		if turn.Speaker == "a" {
			if turn.SessionID != "session-a" || turn.ToPhone != "62822" {
				t.Errorf("turn %d (speaker a): expected SessionID=session-a ToPhone=62822, got SessionID=%s ToPhone=%s", i, turn.SessionID, turn.ToPhone)
			}
		} else {
			if turn.SessionID != "session-b" || turn.ToPhone != "62811" {
				t.Errorf("turn %d (speaker b): expected SessionID=session-b ToPhone=62811, got SessionID=%s ToPhone=%s", i, turn.SessionID, turn.ToPhone)
			}
		}
		if turn.Body != s.Turns[i].Text {
			t.Errorf("turn %d: expected Body %q, got %q", i, s.Turns[i].Text, turn.Body)
		}
	}
}

func TestExpandDrawsEveryDelayInsidePacingAndHonoursPerTurnOverrides(t *testing.T) {
	raw := `{
		"version": 1,
		"pacing": {"min_seconds": 5, "max_seconds": 7},
		"turns": [
			{"speaker": "a", "text": "Satu"},
			{"speaker": "b", "text": "Dua"},
			{"speaker": "a", "text": "Tiga"},
			{"speaker": "b", "text": "Empat", "delay_seconds": 99}
		]
	}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}
	pair := Pair{
		A: Participant{ID: "session-a", Phone: "62811"},
		B: Participant{ID: "session-b", Phone: "62822"},
	}

	sawMin, sawMax := false, false
	for seed := uint64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		turns := Expand(s, pair, rng)

		// turn index 2 (0-based) is the third turn, drawn from pacing since
		// it has no override.
		delay := turns[2].DelaySeconds
		if delay < 5 || delay > 7 {
			t.Fatalf("seed %d: drawn delay %d is outside pacing window [5,7]", seed, delay)
		}
		if delay == 5 {
			sawMin = true
		}
		if delay == 7 {
			sawMax = true
		}

		// the fourth turn's explicit override must survive untouched.
		if turns[3].DelaySeconds != 99 {
			t.Fatalf("seed %d: expected the per-turn override 99 to survive, got %d", seed, turns[3].DelaySeconds)
		}
	}

	if !sawMin {
		t.Errorf("never drew the pacing minimum (5) across 200 seeds")
	}
	if !sawMax {
		t.Errorf("never drew the pacing maximum (7) across 200 seeds")
	}
}

func TestExpandForcesTheFirstTurnsDelayToZero(t *testing.T) {
	raw := `{
		"version": 1,
		"pacing": {"min_seconds": 5, "max_seconds": 7},
		"turns": [
			{"speaker": "a", "text": "Halo", "delay_seconds": 42},
			{"speaker": "b", "text": "Hai"}
		]
	}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}
	pair := Pair{
		A: Participant{ID: "session-a", Phone: "62811"},
		B: Participant{ID: "session-b", Phone: "62822"},
	}
	rng := rand.New(rand.NewPCG(1, 2))

	turns := Expand(s, pair, rng)
	if turns[0].DelaySeconds != 0 {
		t.Fatalf("expected the first turn's delay to be forced to 0 even with an explicit override, got %d", turns[0].DelaySeconds)
	}
}
