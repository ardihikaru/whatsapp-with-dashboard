package corpus

import (
	"regexp"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
)

// TestSceneStagesMatchTheConvoDomainsStageConstants pins this package's
// StageOpener/StageMiddle/StageCloser string-for-string against
// internal/domain/convo's — see corpus.go's doc comment on those constants
// for why corpus keeps its own literals instead of importing convo at
// runtime (it has no other reason to depend on the persistence layer). A
// test-only import here is enough to catch a drift the moment either side
// changes.
func TestSceneStagesMatchTheConvoDomainsStageConstants(t *testing.T) {
	if StageOpener != convo.StageOpener {
		t.Fatalf("corpus.StageOpener = %q, convo.StageOpener = %q", StageOpener, convo.StageOpener)
	}
	if StageMiddle != convo.StageMiddle {
		t.Fatalf("corpus.StageMiddle = %q, convo.StageMiddle = %q", StageMiddle, convo.StageMiddle)
	}
	if StageCloser != convo.StageCloser {
		t.Fatalf("corpus.StageCloser = %q, convo.StageCloser = %q", StageCloser, convo.StageCloser)
	}
}

// selfconvoVarKeyPattern is selfconvo's own (unexported) varKeyRE literal,
// hardcoded here since it cannot be imported — see snakeCaseRE's doc
// comment in corpus.go for why the two are kept in step by hand rather
// than sharing one compiled regexp.
const selfconvoVarKeyPattern = `^[a-z_][a-z0-9_]*$`

// TestSnakeCaseRETracksSelfconvosVarKeyShape pins this package's snake_case
// shape equal to selfconvo's, the same way selfconvo's own topic_test.go
// pins placeholderRE against paraphrase.PlaceholderPattern. Pack.Topic and
// every Slots key become Script.Topic/Script.Vars keys verbatim, so a
// drift here would mean Validate(pack) accepts a shape
// selfconvo.Validate later rejects — exactly the failure mode
// TestEveryGeneratedTemplateBodyPassesSelfconvoValidate exists to catch,
// just cheaper to run.
func TestSnakeCaseRETracksSelfconvosVarKeyShape(t *testing.T) {
	if snakeCaseRE.String() != selfconvoVarKeyPattern {
		t.Fatalf("snakeCaseRE = %q, want %q", snakeCaseRE.String(), selfconvoVarKeyPattern)
	}
	other := regexp.MustCompile(selfconvoVarKeyPattern)
	for _, c := range []string{"futsal", "les_bahasa_inggris", "a1_b", "Futsal", "1futsal", "futsal-malam", ""} {
		if snakeCaseRE.MatchString(c) != other.MatchString(c) {
			t.Fatalf("snakeCaseRE and selfconvoVarKeyPattern disagree on %q", c)
		}
	}
}

func minimalTurns(pairs ...[2]string) []selfconvo.Turn {
	turns := make([]selfconvo.Turn, len(pairs))
	for i, p := range pairs {
		turns[i] = selfconvo.Turn{Speaker: p[0], Text: p[1]}
	}
	return turns
}

// validPack is a small, structurally valid Pack used by every Validate test
// below; each test mutates a copy of it to introduce exactly one problem.
func validPack() Pack {
	return Pack{
		Topic: "test_topic",
		Title: "Test Topic",
		Slots: map[string][]string{"waktu": {"besok", "nanti"}},
		Scenes: []Scene{
			{
				Stage:   StageOpener,
				Name:    "opener",
				Setups:  []Fragment{{Name: "s1", Turns: minimalTurns([2]string{"a", "hai"})}},
				Endings: []Fragment{{Name: "e1", Turns: minimalTurns([2]string{"b", "hai juga"})}},
			},
			{
				Stage:   StageCloser,
				Name:    "closer",
				Setups:  []Fragment{{Name: "s1", Turns: minimalTurns([2]string{"a", "udah ya"})}},
				Endings: []Fragment{{Name: "e1", Turns: minimalTurns([2]string{"b", "oke"})}},
			},
		},
		Links: []PackLink{
			{FromScene: "opener", ToScene: "closer", Weight: 1, MinGapSeconds: 10, MaxGapSeconds: 20},
			{FromScene: "closer", ToScene: "opener", Weight: 1, MinGapSeconds: 100, MaxGapSeconds: 200},
		},
	}
}

func TestValidateAcceptsAStructurallySoundPack(t *testing.T) {
	if err := Validate(validPack()); err != nil {
		t.Fatalf("Validate(validPack()) = %v, want nil", err)
	}
}

func TestValidateRejectsATopicThatIsNotSnakeCase(t *testing.T) {
	p := validPack()
	p.Topic = "Test-Topic"
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a non-snake_case topic")
	}
}

func TestValidateRejectsAPackWithNoOpenerScene(t *testing.T) {
	p := validPack()
	p.Scenes[0].Stage = StageMiddle
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a pack with no opener-stage scene")
	}
}

func TestValidateRejectsASceneWithNoSetups(t *testing.T) {
	p := validPack()
	p.Scenes[0].Setups = nil
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a scene with no setups")
	}
}

func TestValidateRejectsASceneWithNoEndings(t *testing.T) {
	p := validPack()
	p.Scenes[0].Endings = nil
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a scene with no endings")
	}
}

func TestValidateRejectsADuplicateFragmentNameWithinOneList(t *testing.T) {
	p := validPack()
	p.Scenes[0].Setups = append(p.Scenes[0].Setups, Fragment{Name: "s1", Turns: minimalTurns([2]string{"a", "lain"})})
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted two setups sharing a name")
	}
}

func TestValidateRejectsATurnWithAnInvalidSpeaker(t *testing.T) {
	p := validPack()
	p.Scenes[0].Setups[0].Turns[0].Speaker = "c"
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a turn with speaker \"c\"")
	}
}

func TestValidateRejectsALinkReferencingAnUnknownScene(t *testing.T) {
	p := validPack()
	p.Links = append(p.Links, PackLink{FromScene: "opener", ToScene: "nonexistent", Weight: 1, MaxGapSeconds: 10})
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a link whose to_scene does not exist")
	}
}

func TestValidateRejectsALinkWhoseMaxGapIsBelowItsMinGap(t *testing.T) {
	p := validPack()
	p.Links[0].MinGapSeconds = 100
	p.Links[0].MaxGapSeconds = 10
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a link with max_gap_seconds < min_gap_seconds")
	}
}

func TestValidateRejectsAPackWithNoLinksAtAll(t *testing.T) {
	p := validPack()
	p.Links = nil
	if err := Validate(p); err == nil {
		t.Fatal("Validate accepted a pack with no links")
	}
}

// TestPacksLoadsAndValidatesEveryEmbeddedPack is the whole-corpus load
// smoke test: every packs/*.json file must decode with no unknown fields
// and pass Validate. A malformed or structurally broken pack fails here
// long before TestEveryGeneratedTemplateBodyPassesSelfconvoValidate would
// even get a chance to expand it.
func TestPacksLoadsAndValidatesEveryEmbeddedPack(t *testing.T) {
	packs, err := Packs()
	if err != nil {
		t.Fatalf("Packs() = %v, want nil", err)
	}
	if len(packs) < 45 {
		t.Fatalf("Packs() returned %d packs, want at least 45", len(packs))
	}
	seen := map[string]bool{}
	for _, p := range packs {
		if seen[p.Topic] {
			t.Fatalf("topic %q returned twice by Packs()", p.Topic)
		}
		seen[p.Topic] = true
	}
}
