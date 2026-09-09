package selfconvo

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func TestVaryPicksAlternativesAndSubstitutesVarsConsistentlyAcrossTheRun(t *testing.T) {
	raw := `{
		"version": 1,
		"vars": { "place": ["Bandung", "Bogor"] },
		"turns": [
			{ "speaker": "a", "text": "Halo", "alternatives": ["Hai"] },
			{ "speaker": "b", "text": "Mau ke {{place}} gak?" },
			{ "speaker": "a", "text": "Boleh, {{place}} enak tuh" }
		]
	}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}

	var sawHalo, sawHai bool
	for seed := uint64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		out, err := Vary(s, rng)
		if err != nil {
			t.Fatalf("seed %d: Vary returned error: %v", seed, err)
		}
		if len(out.Turns) != 3 {
			t.Fatalf("seed %d: len(Turns) = %d, want 3 (no optional turns here)", seed, len(out.Turns))
		}

		switch out.Turns[0].Text {
		case "Halo":
			sawHalo = true
		case "Hai":
			sawHai = true
		default:
			t.Fatalf("seed %d: turn 0 text = %q, want \"Halo\" or \"Hai\"", seed, out.Turns[0].Text)
		}

		place1 := strings.TrimSuffix(strings.TrimPrefix(out.Turns[1].Text, "Mau ke "), " gak?")
		place2 := strings.TrimSuffix(strings.TrimPrefix(out.Turns[2].Text, "Boleh, "), " enak tuh")
		if place1 != place2 {
			t.Fatalf("seed %d: the same {{place}} resolved to %q in turn 1 but %q in turn 2 within one run",
				seed, place1, place2)
		}
		if place1 != "Bandung" && place1 != "Bogor" {
			t.Fatalf("seed %d: drawn place = %q, want \"Bandung\" or \"Bogor\"", seed, place1)
		}
	}

	if !sawHalo {
		t.Errorf("never observed the original text \"Halo\" for turn 0 across 300 seeds")
	}
	if !sawHai {
		t.Errorf("never observed the alternative \"Hai\" for turn 0 across 300 seeds")
	}
}

func TestVaryNeverDropsBelowTwoTurnsOrRemovesASpeaker(t *testing.T) {
	t.Run("a 2-turn script with both optional at p=0.01 always keeps both", func(t *testing.T) {
		raw := `{"version":1,"turns":[
			{"speaker":"a","text":"Halo","optional":true,"optional_probability":0.01},
			{"speaker":"b","text":"Hai","optional":true,"optional_probability":0.01}
		]}`
		s, err := Validate([]byte(raw))
		if err != nil {
			t.Fatalf("test setup: Validate returned error: %v", err)
		}

		for seed := uint64(0); seed < 200; seed++ {
			rng := rand.New(rand.NewPCG(seed, seed+1))
			out, err := Vary(s, rng)
			if err != nil {
				t.Fatalf("seed %d: Vary returned error: %v", seed, err)
			}
			if len(out.Turns) != 2 {
				t.Fatalf("seed %d: len(Turns) = %d, want 2 (dropping either would fall below 2)", seed, len(out.Turns))
			}
		}
	})

	t.Run("a 3-turn script where speaker b has one optional turn never loses b", func(t *testing.T) {
		raw := `{"version":1,"turns":[
			{"speaker":"a","text":"Satu"},
			{"speaker":"b","text":"Dua","optional":true,"optional_probability":0.01},
			{"speaker":"a","text":"Tiga"}
		]}`
		s, err := Validate([]byte(raw))
		if err != nil {
			t.Fatalf("test setup: Validate returned error: %v", err)
		}

		for seed := uint64(0); seed < 200; seed++ {
			rng := rand.New(rand.NewPCG(seed, seed+1))
			out, err := Vary(s, rng)
			if err != nil {
				t.Fatalf("seed %d: Vary returned error: %v", seed, err)
			}
			var sawB bool
			for _, turn := range out.Turns {
				if turn.Speaker == "b" {
					sawB = true
				}
			}
			if !sawB {
				t.Fatalf("seed %d: speaker b has no turn left in %+v, want b's only turn to be forced kept", seed, out.Turns)
			}
		}
	})
}

func TestVaryIsTheIdentityForAScriptWithoutVariationFields(t *testing.T) {
	raw := `{"version":1,"turns":[
		{"speaker":"a","text":"Halo","delay_seconds":42},
		{"speaker":"b","text":"Hai"}
	]}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}
	rng := rand.New(rand.NewPCG(1, 2))

	out, err := Vary(s, rng)
	if err != nil {
		t.Fatalf("Vary returned error: %v", err)
	}
	if !reflect.DeepEqual(out.Turns, s.Turns) {
		t.Fatalf("Vary changed the turns of a script with no variation fields:\ngot  %+v\nwant %+v", out.Turns, s.Turns)
	}
}

func TestVaryFailsWhenASubstitutionPushesATurnOverTheTextLimit(t *testing.T) {
	longValue := strings.Repeat("a", MaxVarValueChars) // exactly at the vars value cap
	// "{{v}}" is 5 runes; pad the rest of the text so the ORIGINAL turn
	// lands exactly at MaxTextChars (accepted by Validate), but substituting
	// v's 256-rune value in place of the 5-rune placeholder pushes it over.
	text := "{{v}}" + strings.Repeat("b", MaxTextChars-5)
	raw := fmt.Sprintf(`{"version":1,"vars":{"v":["%s"]},"turns":[{"speaker":"a","text":"%s"},{"speaker":"b","text":"Hai"}]}`,
		longValue, text)

	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("test setup: Validate returned error: %v", err)
	}
	rng := rand.New(rand.NewPCG(1, 2))

	_, err = Vary(s, rng)
	if err == nil {
		t.Fatalf("expected Vary to fail when a substitution pushes turn 1 over %d characters, got no error", MaxTextChars)
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected a *ValidationError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "turn 1") {
		t.Fatalf("expected the error to name turn 1, got %q", err.Error())
	}
}
