package selfconvo

import (
	"regexp"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo/paraphrase"
)

// TestPlaceholderPatternMatchesParaphrasePlaceholderPattern pins
// placeholderRE's literal source equal to paraphrase.PlaceholderPattern —
// see placeholderRE's doc comment for why the two can't share a single
// compiled *regexp.Regexp value. A future edit to either literal without
// the other fails this test rather than silently letting the two shapes
// drift apart (paraphrase would then either mangle a placeholder selfconvo
// considers valid, or fail to protect one it should).
func TestPlaceholderPatternMatchesParaphrasePlaceholderPattern(t *testing.T) {
	if placeholderRE.String() != paraphrase.PlaceholderPattern {
		t.Fatalf("selfconvo's placeholderRE = %q, paraphrase.PlaceholderPattern = %q, want identical",
			placeholderRE.String(), paraphrase.PlaceholderPattern)
	}
	// Belt-and-braces: also confirm the two compiled forms accept exactly
	// the same set of representative strings, not just the same source
	// text (in case a future refactor compiles one of them with extra
	// flags).
	other := regexp.MustCompile(paraphrase.PlaceholderPattern)
	cases := []string{"{{nama}}", "{{ place }}", "{{a1_b}}", "{{Nama}}", "{nama}", "{{1nama}}", "plain text"}
	for _, c := range cases {
		if placeholderRE.MatchString(c) != other.MatchString(c) {
			t.Fatalf("placeholderRE and paraphrase.PlaceholderPattern disagree on %q", c)
		}
	}
}

func TestValidateAcceptsAnOmittedTopic(t *testing.T) {
	s, err := Validate([]byte(minimalValidScript))
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if s.Topic != "" {
		t.Fatalf("Topic = %q, want empty when the script omits it", s.Topic)
	}
}

func TestValidateAcceptsAWellFormedTopic(t *testing.T) {
	raw := `{"version":1,"topic":"food","turns":[
		{"speaker":"a","text":"Halo"},
		{"speaker":"b","text":"Hai"}
	]}`
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if s.Topic != "food" {
		t.Fatalf("Topic = %q, want %q", s.Topic, "food")
	}
}

func TestValidateRejectsATopicThatIsNotLowercaseSnakeCase(t *testing.T) {
	cases := []string{"Food", "food-truck", "food truck", "123food"}
	for _, topic := range cases {
		raw := `{"version":1,"topic":"` + topic + `","turns":[
			{"speaker":"a","text":"Halo"},
			{"speaker":"b","text":"Hai"}
		]}`
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("Validate accepted topic %q, want a *ValidationError (not lowercase snake_case)", topic)
		}
	}
}

func TestValidateRejectsATopicOverFortyEightChars(t *testing.T) {
	topic := ""
	for i := 0; i < 49; i++ {
		topic += "a"
	}
	raw := `{"version":1,"topic":"` + topic + `","turns":[
		{"speaker":"a","text":"Halo"},
		{"speaker":"b","text":"Hai"}
	]}`
	if _, err := Validate([]byte(raw)); err == nil {
		t.Fatalf("Validate accepted a %d-char topic, want a *ValidationError over the %d limit", len(topic), MaxTopicChars)
	}
}
