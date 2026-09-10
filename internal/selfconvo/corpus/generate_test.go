package corpus

import (
	"reflect"
	"strings"
	"testing"
)

// loadPacksOrFatal is the fixture every test below shares: the real,
// embedded corpus, loaded and structurally validated once per test (Go's
// testing package does not share state across tests, and Packs() is cheap
// enough — see corpus_test.go's own TestPacksLoadsAndValidatesEveryEmbeddedPack — that
// re-running it per test is not worth a package-level var and the ordering
// hazard that would bring).
func loadPacksOrFatal(t *testing.T) []Pack {
	t.Helper()
	packs, err := Packs()
	if err != nil {
		t.Fatalf("Packs() = %v, want nil", err)
	}
	return packs
}

// normalizedBody is what "distinct" means for the corpus's ≥90% check
// below: the sequence of (speaker, text) turns a run would actually send,
// lowercased and whitespace-trimmed — never the JSON bytes (which would
// count two runs of gofmt/marshal-order noise as "different") and never
// the Name (which is unique by construction and would make every template
// trivially "distinct", defeating the whole point of the check).
func normalizedBody(g Generated) string {
	var b strings.Builder
	for _, turn := range g.Script.Turns {
		b.WriteString(strings.ToLower(strings.TrimSpace(turn.Speaker)))
		b.WriteByte(':')
		b.WriteString(strings.ToLower(strings.TrimSpace(turn.Text)))
		b.WriteByte('|')
	}
	return b.String()
}

func TestEveryGeneratedTemplateBodyPassesSelfconvoValidate(t *testing.T) {
	packs := loadPacksOrFatal(t)
	// Generate itself calls selfconvo.Validate on every template it builds
	// and returns an error the moment one fails (see generate.go) — so a
	// successful call already IS this test's claim for the whole corpus,
	// not a sample of it. Asserting err == nil here is deliberately the
	// entire test: a regression would turn this into a build-time failure
	// message naming the exact offending template.
	if _, err := Generate(packs, 1); err != nil {
		t.Fatalf("Generate produced a template that failed selfconvo.Validate: %v", err)
	}
}

func TestGenerateProducesAtLeast2000TemplatesWithAtLeast90PercentDistinctBodies(t *testing.T) {
	packs := loadPacksOrFatal(t)
	generated, err := Generate(packs, 1)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(generated) < 2000 {
		t.Fatalf("Generate produced %d templates, want at least 2000", len(generated))
	}

	distinct := map[string]bool{}
	for _, g := range generated {
		distinct[normalizedBody(g)] = true
	}
	ratio := float64(len(distinct)) / float64(len(generated))
	if ratio < 0.90 {
		t.Fatalf("only %d/%d (%.1f%%) generated bodies are distinct, want at least 90%%",
			len(distinct), len(generated), ratio*100)
	}
	t.Logf("generated %d templates, %d distinct bodies (%.1f%%)", len(generated), len(distinct), ratio*100)
}

func TestGenerateIsDeterministicForASeed(t *testing.T) {
	packs := loadPacksOrFatal(t)
	a, err := Generate(packs, 42)
	if err != nil {
		t.Fatalf("Generate(42) #1: %v", err)
	}
	b, err := Generate(packs, 42)
	if err != nil {
		t.Fatalf("Generate(42) #2: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Generate(packs, 42) produced two different results across two calls")
	}
}

func TestGenerateProducesADifferentResultForADifferentSeed(t *testing.T) {
	packs := loadPacksOrFatal(t)
	a, err := Generate(packs, 42)
	if err != nil {
		t.Fatalf("Generate(42): %v", err)
	}
	b, err := Generate(packs, 43)
	if err != nil {
		t.Fatalf("Generate(43): %v", err)
	}
	if reflect.DeepEqual(a, b) {
		t.Fatal("Generate produced byte-identical output for two different seeds")
	}
}

func TestEveryGeneratedLinkResolvesToAGeneratedTemplateName(t *testing.T) {
	packs := loadPacksOrFatal(t)
	generated, err := Generate(packs, 1)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	names := map[string]bool{}
	for _, g := range generated {
		names[g.Name] = true
	}
	for _, g := range generated {
		for _, l := range g.Links {
			if !names[l.ToName] {
				t.Fatalf("template %q links to %q, which Generate never produced", g.Name, l.ToName)
			}
			if l.MaxGapSeconds < l.MinGapSeconds {
				t.Fatalf("template %q -> %q: max_gap_seconds %d < min_gap_seconds %d",
					g.Name, l.ToName, l.MaxGapSeconds, l.MinGapSeconds)
			}
		}
	}
}

func TestEveryGeneratedTemplateTopicAndStageMatchItsPack(t *testing.T) {
	packs := loadPacksOrFatal(t)
	packByTopic := map[string]Pack{}
	for _, p := range packs {
		packByTopic[p.Topic] = p
	}
	generated, err := Generate(packs, 1)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	validStage := map[string]bool{StageOpener: true, StageMiddle: true, StageCloser: true}
	for _, g := range generated {
		if _, ok := packByTopic[g.Topic]; !ok {
			t.Fatalf("template %q has topic %q, which is not any loaded pack's topic", g.Name, g.Topic)
		}
		if g.Script.Topic != g.Topic {
			t.Fatalf("template %q: Script.Topic = %q, want it to equal Topic %q", g.Name, g.Script.Topic, g.Topic)
		}
		if !validStage[g.Stage] {
			t.Fatalf("template %q has stage %q, want one of opener/middle/closer", g.Name, g.Stage)
		}
	}
}

// TestEveryTopicsGraphIsReachableFromAnOpener walks every GeneratedLink
// edge starting from each topic's opener-stage templates and asserts every
// other template of that topic is reachable. A topic whose middle or
// closer templates can never be reached from an opener is a pack-authoring
// bug (an orphaned scene a schedule could never actually play), not
// something the runtime should have to notice.
func TestEveryTopicsGraphIsReachableFromAnOpener(t *testing.T) {
	packs := loadPacksOrFatal(t)
	generated, err := Generate(packs, 1)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	byTopic := map[string][]Generated{}
	for _, g := range generated {
		byTopic[g.Topic] = append(byTopic[g.Topic], g)
	}

	for topic, templates := range byTopic {
		byName := map[string]Generated{}
		for _, g := range templates {
			byName[g.Name] = g
		}

		var queue []string
		reached := map[string]bool{}
		for _, g := range templates {
			if g.Stage == StageOpener {
				queue = append(queue, g.Name)
				reached[g.Name] = true
			}
		}
		if len(queue) == 0 {
			t.Fatalf("topic %q has no opener-stage template at all", topic)
		}
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			for _, l := range byName[name].Links {
				if !reached[l.ToName] {
					reached[l.ToName] = true
					queue = append(queue, l.ToName)
				}
			}
		}

		for _, g := range templates {
			if !reached[g.Name] {
				t.Fatalf("topic %q: template %q is not reachable from any opener-stage template", topic, g.Name)
			}
		}
	}
}
