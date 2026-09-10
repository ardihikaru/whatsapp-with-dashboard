// Package corpus turns a small number of hand-authored "topic packs" into a
// large, varied set of concrete Self Convo templates.
//
// A pack (internal/selfconvo/corpus/packs/*.json) describes one everyday
// topic as a SLOT GRAMMAR rather than a list of finished scripts:
//
//   - slots are named lists of interchangeable concrete values (a place, a
//     day, a complication, ...). They become Script.Vars verbatim — resolved
//     at RUN time by selfconvo.Vary, exactly the way an operator-authored
//     template's vars are — never string-formatted into the pack's text at
//     generation time. That is what keeps the template count this package
//     produces bounded instead of exploding with every slot combination.
//   - a scene is one stage of a topic's arc (opener/middle/closer) built
//     from two independently authored fragment pools: setups (how the scene
//     can start) and endings (how it can resolve). Generate takes the FULL
//     cross product of a scene's setups and endings, so a small number of
//     authored fragments produces a much larger number of genuinely
//     different conversations — this is the "bounded expansion over an axis
//     that changes a conversation's shape" the package exists to do, not
//     padding: two combos differ in their setup, their ending, or both, so
//     their turn sequences are never byte-identical.
//   - links describe which scene of a topic plausibly follows which, with a
//     weight and a gap window; Generate expands them into per-template
//     edges (see GeneratedLink), keyed by the generated template's NAME so
//     the seeder can resolve them to real ids after insertion.
//
// This package never touches the database — internal/domain/convo owns
// that — and it never duplicates internal/selfconvo/paraphrase's job of
// varying WORDING; a pack's fragments are meant to read as one clean way of
// saying something, with paraphrase (a run-time step, see vary.go) doing the
// register/synonym/particle variation on top.
package corpus

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
)

// Stage values a Scene may declare. Mirrors
// internal/domain/convo/schedule_model.go's StageOpener/StageMiddle/
// StageCloser constants (string-for-string, pinned by
// TestSceneStagesMatchTheConvoDomainsStageConstants) rather than importing
// that package: corpus has no reason to depend on the app's persistence
// layer, and the three strings are exactly as stable as the DB column that
// stores them.
const (
	StageOpener = "opener"
	StageMiddle = "middle"
	StageCloser = "closer"
)

// snakeCaseRE is the shape required of a Pack.Topic and every Slots key —
// the same shape selfconvo.Validate requires of Script.Topic and Script.Vars
// keys (its varKeyRE is unexported, so this is a separate literal; both are
// `^[a-z_][a-z0-9_]*$` and TestSnakeCaseRETracksSelfconvosVarKeyShape pins
// them equal).
var snakeCaseRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// MaxTopicChars mirrors selfconvo.MaxTopicChars — a pack's Topic becomes
// Script.Topic verbatim, so it must fit under the same limit or every
// template Generate produces for that pack would fail selfconvo.Validate.
const MaxTopicChars = selfconvo.MaxTopicChars

// Pack is one topic's grammar, decoded from a packs/*.json file.
type Pack struct {
	// Topic is lowercase snake_case, at most MaxTopicChars runes — it
	// becomes every generated template's Script.Topic and Stage grouping
	// key (internal/domain/convo.Template.Topic).
	Topic string `json:"topic"`
	// Title is a human-readable label; never sent, never validated by
	// selfconvo — purely documentation for whoever reads the pack file.
	Title string `json:"title"`
	// Slots become Script.Vars, unchanged, on every template this pack
	// produces. Keys are lowercase snake_case (selfconvo's placeholder
	// shape); values are the concrete strings a `{{key}}` in a fragment's
	// text resolves to at run time.
	Slots map[string][]string `json:"slots"`
	// Scenes is the topic's arc: each names one stage of the conversation
	// and the fragments Generate crosses to produce that stage's
	// templates.
	Scenes []Scene `json:"scenes"`
	// Links describes which scene plausibly follows which. See PackLink.
	Links []PackLink `json:"links"`
}

// Scene is one stage of a Pack's arc — e.g. futsal's "ajak_main" (opener),
// "cek_jadi_gak" (middle) or "abis_main" (closer). Generate crosses every
// Setup against every Ending to produce that many templates for this scene
// alone.
type Scene struct {
	// Stage is one of StageOpener/StageMiddle/StageCloser.
	Stage string `json:"stage"`
	// Name identifies the scene within its pack — unique per pack, and
	// what PackLink.FromScene/ToScene reference.
	Name string `json:"name"`
	// Pacing overrides selfconvo's default pacing window for every
	// template this scene produces. Nil means selfconvo.Validate applies
	// its own default (DefaultMinSeconds/DefaultMaxSeconds).
	Pacing *selfconvo.Pacing `json:"pacing,omitempty"`
	// Setups are the ways this scene's conversation can start.
	Setups []Fragment `json:"setups"`
	// Endings are the ways this scene's conversation can resolve.
	Endings []Fragment `json:"endings"`
}

// Fragment is a short, self-contained run of turns — half of one scene
// combination. Turns reuses selfconvo.Turn directly (rather than a parallel
// struct) so a fragment's JSON shape is exactly a Script's, and so
// Generate's composed turn list needs no field-by-field conversion.
type Fragment struct {
	// Name identifies the fragment within its scene's Setups or Endings —
	// unique within each list, and what a generated template's Name
	// records (as "<setup>+<ending>") so a human can tell two templates
	// of the same scene apart.
	Name  string           `json:"name"`
	Turns []selfconvo.Turn `json:"turns"`
}

// PackLink is one topic-authored transition rule: after ANY template of
// FromScene, some template of ToScene is a plausible next conversation.
// Generate expands this into concrete per-template GeneratedLink edges (see
// generate.go) — one PackLink can produce many edges, since a scene usually
// has many generated templates.
type PackLink struct {
	FromScene     string `json:"from_scene"`
	ToScene       string `json:"to_scene"`
	Weight        int    `json:"weight"`
	MinGapSeconds int    `json:"min_gap_seconds"`
	MaxGapSeconds int    `json:"max_gap_seconds"`
}

// Validate checks p's structure — the shape Generate needs to safely cross
// setups with endings and resolve links — without running selfconvo.Validate
// over every combination it could produce; that whole-corpus check belongs
// to the caller once combinations exist (see TestEveryGeneratedTemplateBody
// PassesSelfconvoValidate), because it is comparatively expensive and this
// function must stay cheap enough to run over every pack at package init.
func Validate(p Pack) error {
	if p.Topic == "" {
		return fmt.Errorf("pack: topic is empty")
	}
	if rc := len([]rune(p.Topic)); rc > MaxTopicChars {
		return fmt.Errorf("pack %q: topic is %d characters, over the %d limit", p.Topic, rc, MaxTopicChars)
	}
	if !snakeCaseRE.MatchString(p.Topic) {
		return fmt.Errorf("pack %q: topic must be lowercase snake_case (match %s)", p.Topic, snakeCaseRE.String())
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("pack %q: title is empty", p.Topic)
	}
	for key, values := range p.Slots {
		if !snakeCaseRE.MatchString(key) {
			return fmt.Errorf("pack %q: slot key %q must be lowercase snake_case", p.Topic, key)
		}
		if len(values) == 0 {
			return fmt.Errorf("pack %q: slot %q has no values", p.Topic, key)
		}
		for i, v := range values {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("pack %q: slot %q value %d is empty", p.Topic, key, i+1)
			}
		}
	}
	if len(p.Scenes) == 0 {
		return fmt.Errorf("pack %q: has no scenes", p.Topic)
	}
	seenScene := map[string]bool{}
	hasOpener := false
	for _, sc := range p.Scenes {
		if sc.Name == "" {
			return fmt.Errorf("pack %q: a scene has no name", p.Topic)
		}
		if seenScene[sc.Name] {
			return fmt.Errorf("pack %q: scene name %q is duplicated", p.Topic, sc.Name)
		}
		seenScene[sc.Name] = true
		switch sc.Stage {
		case StageOpener:
			hasOpener = true
		case StageMiddle, StageCloser:
			// fine
		default:
			return fmt.Errorf("pack %q scene %q: stage %q must be one of %q, %q, %q",
				p.Topic, sc.Name, sc.Stage, StageOpener, StageMiddle, StageCloser)
		}
		if len(sc.Setups) == 0 {
			return fmt.Errorf("pack %q scene %q: has no setups", p.Topic, sc.Name)
		}
		if len(sc.Endings) == 0 {
			return fmt.Errorf("pack %q scene %q: has no endings", p.Topic, sc.Name)
		}
		if err := validateFragments(p.Topic, sc.Name, "setups", sc.Setups); err != nil {
			return err
		}
		if err := validateFragments(p.Topic, sc.Name, "endings", sc.Endings); err != nil {
			return err
		}
	}
	if !hasOpener {
		return fmt.Errorf("pack %q: has no scene with stage %q", p.Topic, StageOpener)
	}
	if len(p.Links) == 0 {
		return fmt.Errorf("pack %q: has no links", p.Topic)
	}
	for i, l := range p.Links {
		if !seenScene[l.FromScene] {
			return fmt.Errorf("pack %q: links[%d].from_scene %q is not a scene in this pack", p.Topic, i, l.FromScene)
		}
		if !seenScene[l.ToScene] {
			return fmt.Errorf("pack %q: links[%d].to_scene %q is not a scene in this pack", p.Topic, i, l.ToScene)
		}
		if l.Weight <= 0 {
			return fmt.Errorf("pack %q: links[%d] weight must be positive, got %d", p.Topic, i, l.Weight)
		}
		if l.MinGapSeconds < 0 || l.MaxGapSeconds < l.MinGapSeconds {
			return fmt.Errorf("pack %q: links[%d] must satisfy 0 <= min_gap_seconds <= max_gap_seconds, got min=%d max=%d",
				p.Topic, i, l.MinGapSeconds, l.MaxGapSeconds)
		}
	}
	return nil
}

func validateFragments(topic, scene, kind string, frags []Fragment) error {
	seen := map[string]bool{}
	for i, f := range frags {
		if f.Name == "" {
			return fmt.Errorf("pack %q scene %q: %s[%d] has no name", topic, scene, kind, i)
		}
		if seen[f.Name] {
			return fmt.Errorf("pack %q scene %q: %s name %q is duplicated", topic, scene, kind, f.Name)
		}
		seen[f.Name] = true
		if len(f.Turns) == 0 {
			return fmt.Errorf("pack %q scene %q: %s %q has no turns", topic, scene, kind, f.Name)
		}
		for j, t := range f.Turns {
			speaker := strings.ToLower(strings.TrimSpace(t.Speaker))
			if speaker != "a" && speaker != "b" {
				return fmt.Errorf("pack %q scene %q: %s %q turn %d: speaker must be \"a\" or \"b\", got %q",
					topic, scene, kind, f.Name, j+1, t.Speaker)
			}
			if strings.TrimSpace(t.Text) == "" {
				return fmt.Errorf("pack %q scene %q: %s %q turn %d: text is empty",
					topic, scene, kind, f.Name, j+1)
			}
		}
	}
	return nil
}
