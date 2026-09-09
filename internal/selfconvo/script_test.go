package selfconvo

import (
	"strings"
	"testing"
)

// minimalValidScript is a small two-speaker script Validate should accept
// as-is; individual tests mutate a copy of this JSON to poke at one rule at
// a time.
const minimalValidScript = `{
  "version": 1,
  "name": "Minimal",
  "turns": [
    { "speaker": "a", "text": "Halo" },
    { "speaker": "b", "text": "Hai juga" }
  ]
}`

func TestValidateAcceptsTheEmbeddedExample(t *testing.T) {
	s, err := Validate(ExampleJSON)
	if err != nil {
		t.Fatalf("Validate(ExampleJSON) returned error: %v", err)
	}
	if s.Pacing == nil {
		t.Fatalf("expected pacing to survive validation, got nil")
	}
	if s.Pacing.MinSeconds != 20 || s.Pacing.MaxSeconds != 90 {
		t.Fatalf("expected pacing 20/90, got %d/%d", s.Pacing.MinSeconds, s.Pacing.MaxSeconds)
	}
	if len(s.Turns) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(s.Turns))
	}
	var sawOverride bool
	for _, turn := range s.Turns {
		if turn.DelaySeconds != nil && *turn.DelaySeconds == 15 {
			sawOverride = true
		}
	}
	if !sawOverride {
		t.Fatalf("expected the embedded example to keep its delay_seconds:15 override, got %+v", s.Turns)
	}

	if len(s.Vars) == 0 {
		t.Fatalf("expected the embedded example to demonstrate at least one vars entry, got none")
	}
	var sawAlternative bool
	for _, turn := range s.Turns {
		if len(turn.Alternatives) > 0 {
			sawAlternative = true
		}
	}
	if !sawAlternative {
		t.Fatalf("expected the embedded example to demonstrate at least one turn with alternatives, got none")
	}
}

func TestValidateRequiresAtLeastTwoTurnsAndBothSpeakers(t *testing.T) {
	t.Run("fewer than two turns", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected an error for a single-turn script, got none")
		}
	})

	t.Run("only speaker a appears", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"a","text":"Masih di sini"}]}`
		_, err := Validate([]byte(raw))
		if err == nil {
			t.Fatalf("expected an error when speaker b never appears, got none")
		}
		if !strings.Contains(err.Error(), "speaker") {
			t.Fatalf("expected the error to name the missing speaker, got %q", err.Error())
		}
	})
}

func TestValidateRejectsASpeakerOtherThanAOrB(t *testing.T) {
	raw := `{"version":1,"turns":[{"speaker":"c","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
	_, err := Validate([]byte(raw))
	if err == nil {
		t.Fatalf("expected an error for speaker \"c\", got none")
	}
	if !strings.Contains(err.Error(), "turn 1") {
		t.Fatalf("expected the error to name turn 1, got %q", err.Error())
	}
}

func TestValidateRejectsTextOver4096Chars(t *testing.T) {
	// Use a multibyte rune (é, 2 bytes in UTF-8) to prove the limit counts
	// runes, not bytes: 4096 of them is well under any byte-based cap that
	// would otherwise mask this test.
	okText := strings.Repeat("é", MaxTextChars)
	tooLongText := strings.Repeat("é", MaxTextChars+1)

	build := func(text string) string {
		return `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"` + text + `"}]}`
	}

	if _, err := Validate([]byte(build(okText))); err != nil {
		t.Fatalf("expected exactly %d runes to be accepted, got error: %v", MaxTextChars, err)
	}
	if _, err := Validate([]byte(build(tooLongText))); err == nil {
		t.Fatalf("expected %d runes to be rejected, got none", MaxTextChars+1)
	}
}

func TestValidateRejectsPacingWithMinAboveMaxAndFillsTheDefaultWhenAbsent(t *testing.T) {
	t.Run("min above max is rejected", func(t *testing.T) {
		raw := `{"version":1,"pacing":{"min_seconds":90,"max_seconds":20},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected an error when min_seconds > max_seconds, got none")
		}
	})

	t.Run("absent pacing is filled with the default", func(t *testing.T) {
		s, err := Validate([]byte(minimalValidScript))
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if s.Pacing == nil {
			t.Fatalf("expected a default pacing to be filled in, got nil")
		}
		if s.Pacing.MinSeconds != DefaultMinSeconds || s.Pacing.MaxSeconds != DefaultMaxSeconds {
			t.Fatalf("expected default pacing %d/%d, got %d/%d", DefaultMinSeconds, DefaultMaxSeconds, s.Pacing.MinSeconds, s.Pacing.MaxSeconds)
		}
	})
}

func TestValidateRejectsUnknownFields(t *testing.T) {
	raw := `{"version":1,"unknown_field":true,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
	if _, err := Validate([]byte(raw)); err == nil {
		t.Fatalf("expected an error for an unknown top-level field, got none")
	}
}

func TestValidateRejectsABodyOverTheSizeCap(t *testing.T) {
	// Pad a valid script's text with enough filler to push the whole body
	// past MaxTemplateBytes while remaining syntactically valid JSON, so the
	// size check — not a JSON-parse failure — is what is being exercised.
	filler := strings.Repeat("a", MaxTemplateBytes)
	raw := `{"version":1,"turns":[{"speaker":"a","text":"` + filler + `"},{"speaker":"b","text":"Hai"}]}`
	if len(raw) <= MaxTemplateBytes {
		t.Fatalf("test setup bug: raw body is %d bytes, not over the %d cap", len(raw), MaxTemplateBytes)
	}
	if _, err := Validate([]byte(raw)); err == nil {
		t.Fatalf("expected an error for a body over %d bytes, got none", MaxTemplateBytes)
	}
}

func TestValidateAcceptsVariationFields(t *testing.T) {
	raw := `{
  "version": 1,
  "vars": { "place": ["Bandung", "Bogor"] },
  "turns": [
    { "speaker": "a", "text": "Halo, sibuk gak Sabtu?" },
    { "speaker": "b", "text": "Enggak, kenapa?", "alternatives": ["Lagi santai, ada apa?"] },
    { "speaker": "a", "text": "Mau ke {{place}} gak?", "optional": true, "optional_probability": 0.7 },
    { "speaker": "b", "text": "Boleh, jam berapa?" }
  ]
}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if got := s.Vars["place"]; len(got) != 2 || got[0] != "Bandung" || got[1] != "Bogor" {
		t.Fatalf("Vars[place] = %v, want [Bandung Bogor]", got)
	}
	if len(s.Turns[1].Alternatives) != 1 || s.Turns[1].Alternatives[0] != "Lagi santai, ada apa?" {
		t.Fatalf("Turns[1].Alternatives = %v, want the single alternative to survive", s.Turns[1].Alternatives)
	}
	if !s.Turns[2].Optional {
		t.Fatalf("Turns[2].Optional = false, want true")
	}
	if s.Turns[2].OptionalProbability == nil || *s.Turns[2].OptionalProbability != 0.7 {
		t.Fatalf("Turns[2].OptionalProbability = %v, want 0.7", s.Turns[2].OptionalProbability)
	}
}

func TestValidateRejectsAPlaceholderWithNoMatchingVar(t *testing.T) {
	raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Mau ke {{place}} gak?"}]}`
	_, err := Validate([]byte(raw))
	if err == nil {
		t.Fatalf("expected an error for an undeclared {{place}} placeholder, got none")
	}
	if !strings.Contains(err.Error(), "turn 2") || !strings.Contains(err.Error(), "place") {
		t.Fatalf("expected the error to name turn 2 and the key %q, got %q", "place", err.Error())
	}

	t.Run("declared in vars is accepted", func(t *testing.T) {
		raw := `{"version":1,"vars":{"place":["Bandung"]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Mau ke {{place}} gak?"}]}`
		if _, err := Validate([]byte(raw)); err != nil {
			t.Fatalf("expected a declared placeholder to be accepted, got error: %v", err)
		}
	})

	t.Run("declared only in an alternative is checked too", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai","alternatives":["Mau ke {{place}} gak?"]}]}`
		_, err := Validate([]byte(raw))
		if err == nil {
			t.Fatalf("expected an error for an undeclared placeholder inside alternatives, got none")
		}
		if !strings.Contains(err.Error(), "place") {
			t.Fatalf("expected the error to name the key %q, got %q", "place", err.Error())
		}
	})
}

func TestValidateRejectsABadVarKeyOrEmptyValue(t *testing.T) {
	t.Run("uppercase key", func(t *testing.T) {
		raw := `{"version":1,"vars":{"Place":["Bandung"]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected an uppercase vars key to be rejected, got none")
		}
	})

	t.Run("key starting with a digit", func(t *testing.T) {
		raw := `{"version":1,"vars":{"1place":["Bandung"]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected a vars key starting with a digit to be rejected, got none")
		}
	})

	t.Run("empty value list", func(t *testing.T) {
		raw := `{"version":1,"vars":{"place":[]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected an empty vars value list to be rejected, got none")
		}
	})

	t.Run("blank value", func(t *testing.T) {
		raw := `{"version":1,"vars":{"place":["   "]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected a blank vars value to be rejected, got none")
		}
	})

	t.Run("value over the char limit", func(t *testing.T) {
		tooLong := strings.Repeat("a", MaxVarValueChars+1)
		raw := `{"version":1,"vars":{"place":["` + tooLong + `"]},"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai"}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected a vars value over %d chars to be rejected, got none", MaxVarValueChars)
		}
	})
}

func TestValidateRejectsAnOptionalProbabilityOutsideZeroOne(t *testing.T) {
	t.Run("zero is rejected", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai","optional":true,"optional_probability":0}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected optional_probability 0 to be rejected, got none")
		}
	})

	t.Run("above one is rejected", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai","optional":true,"optional_probability":1.5}]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("expected optional_probability 1.5 to be rejected, got none")
		}
	})

	t.Run("one is accepted", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai","optional":true,"optional_probability":1}]}`
		if _, err := Validate([]byte(raw)); err != nil {
			t.Fatalf("expected optional_probability 1 to be accepted, got error: %v", err)
		}
	})

	t.Run("set without optional:true is rejected", func(t *testing.T) {
		raw := `{"version":1,"turns":[{"speaker":"a","text":"Halo"},{"speaker":"b","text":"Hai","optional_probability":0.5}]}`
		_, err := Validate([]byte(raw))
		if err == nil {
			t.Fatalf("expected optional_probability without optional:true to be rejected, got none")
		}
		if !strings.Contains(err.Error(), "optional") {
			t.Fatalf("expected the error to mention optional:true, got %q", err.Error())
		}
	})
}
