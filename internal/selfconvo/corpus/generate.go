package corpus

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
)

// branchFactor bounds how many EXTRA (beyond the reachability-guaranteeing
// base edge — see assignLinks) outgoing edges Generate draws per source
// template for one PackLink. Kept small and fixed: this is variety, not the
// mechanism reachability depends on, so it never needs to scale with a
// scene's size.
const branchFactor = 2

// weightJitter bounds the +0..N adjustment Generate draws per edge on top
// of the pack-authored PackLink.Weight, purely so two different seeds
// produce a measurably different corpus even on a topic small enough that
// every edge's target happens to coincide (see
// TestGenerateProducesADifferentResultForADifferentSeed).
const weightJitter = 3

// Generated is one concrete template Generate produced, plus the edges
// leaving it (by generated-template NAME — the caller resolves names to
// database ids after every template is inserted).
type Generated struct {
	Name   string
	Topic  string
	Stage  string
	Script selfconvo.Script
	// Body is the exact JSON bytes (as a string) selfconvo.Validate
	// accepted for Script — what a caller stores verbatim in
	// convo.Template.Body.
	Body  string
	Links []GeneratedLink
}

// GeneratedLink is one outgoing edge of a Generated template.
type GeneratedLink struct {
	ToName        string
	Weight        int
	MinGapSeconds int
	MaxGapSeconds int
}

// Generate expands every pack in packs into concrete templates. It is
// deterministic for a given seed (same packs, same seed, byte-identical
// result — see TestGenerateIsDeterministicForASeed) and differs across
// seeds only in which OPTIONAL extra links get drawn and their weight
// jitter (see assignLinks) — the set of templates themselves, and the base
// edges that guarantee every topic is reachable from an opener, never
// depend on seed at all.
//
// Every returned Generated's Body has already passed selfconvo.Validate
// (Generate calls it itself, on the exact bytes it stores) — a pack whose
// fragments compose into an invalid script is a corpus bug, and Generate
// reports it as an error rather than silently emitting a template the
// runner would reject later.
func Generate(packs []Pack, seed uint64) ([]Generated, error) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))

	// Iterate packs and scenes in a fixed, seed-independent order — Packs()
	// already sorts by topic, and each pack's own Scenes/Setups/Endings/
	// Links keep their authored (JSON) order — so the SEQUENCE of rng draws
	// below depends only on the pack contents and seed, never on map
	// iteration order or anything else non-deterministic.
	var out []Generated
	byName := map[string]int{}                     // generated template name -> index into out
	sceneNames := map[string]map[string][]string{} // topic -> scene -> template names

	for _, p := range packs {
		sceneNames[p.Topic] = map[string][]string{}
		for _, sc := range p.Scenes {
			pacing := sc.Pacing
			for _, setup := range sc.Setups {
				for _, ending := range sc.Endings {
					name := fmt.Sprintf("%s:%s:%s+%s", p.Topic, sc.Name, setup.Name, ending.Name)
					turns := make([]selfconvo.Turn, 0, len(setup.Turns)+len(ending.Turns))
					turns = append(turns, cloneTurns(setup.Turns)...)
					turns = append(turns, cloneTurns(ending.Turns)...)

					script := selfconvo.Script{
						Version: 1,
						Name:    name,
						Topic:   p.Topic,
						Pacing:  pacing,
						Vars:    cloneSlots(p.Slots),
						Turns:   turns,
					}
					raw, err := json.Marshal(script)
					if err != nil {
						return nil, fmt.Errorf("corpus: marshal %s: %w", name, err)
					}
					validated, err := selfconvo.Validate(raw)
					if err != nil {
						return nil, fmt.Errorf("corpus: generated template %q failed selfconvo.Validate: %w", name, err)
					}

					if _, dup := byName[name]; dup {
						return nil, fmt.Errorf("corpus: template name %q generated twice", name)
					}
					byName[name] = len(out)
					out = append(out, Generated{
						Name:   name,
						Topic:  p.Topic,
						Stage:  sc.Stage,
						Script: validated,
						Body:   string(raw),
					})
					sceneNames[p.Topic][sc.Name] = append(sceneNames[p.Topic][sc.Name], name)
				}
			}
		}

		for _, link := range p.Links {
			fromNames := sceneNames[p.Topic][link.FromScene]
			toNames := sceneNames[p.Topic][link.ToScene]
			assignLinks(out, byName, fromNames, toNames, link, rng)
		}
	}

	return out, nil
}

// assignLinks wires one PackLink's from-scene templates to its to-scene
// templates in two passes, mutating out in place (via byName's indices):
//
//  1. a BASE assignment, entirely seed-independent: the i-th to-template is
//     linked from the (i mod len(fromNames))-th from-template. This alone
//     guarantees every to-template has at least one incoming edge from
//     SOME from-template — which is exactly what
//     TestEveryTopicsGraphIsReachableFromAnOpener depends on, since chained
//     across a topic's whole arc it makes every scene's templates reachable
//     from the previous scene's, and transitively from an opener.
//  2. up to branchFactor EXTRA edges per from-template, seeded, to distinct
//     to-templates not already linked from it — pure variety, is what
//     makes Generate's OUTPUT (not its template set) differ across seeds.
//
// A (from, to) pair is never linked twice — wa_convo_template_links'
// primary key is (from_template_id, to_template_id), so a duplicate edge
// would fail the insert.
func assignLinks(out []Generated, byName map[string]int, fromNames, toNames []string, link PackLink, rng *rand.Rand) {
	if len(fromNames) == 0 || len(toNames) == 0 {
		return
	}
	linked := make(map[string]map[string]bool, len(fromNames))
	addEdge := func(from, to string) {
		if linked[from] == nil {
			linked[from] = map[string]bool{}
		}
		if linked[from][to] {
			return
		}
		linked[from][to] = true
		w := link.Weight + rng.IntN(weightJitter+1)
		idx := byName[from]
		out[idx].Links = append(out[idx].Links, GeneratedLink{
			ToName:        to,
			Weight:        w,
			MinGapSeconds: link.MinGapSeconds,
			MaxGapSeconds: link.MaxGapSeconds,
		})
	}

	for i, to := range toNames {
		from := fromNames[i%len(fromNames)]
		addEdge(from, to)
	}

	for _, from := range fromNames {
		for k := 0; k < branchFactor; k++ {
			to := toNames[rng.IntN(len(toNames))]
			addEdge(from, to)
		}
	}
}

func cloneTurns(turns []selfconvo.Turn) []selfconvo.Turn {
	out := make([]selfconvo.Turn, len(turns))
	for i, t := range turns {
		out[i] = t
		if t.Alternatives != nil {
			out[i].Alternatives = append([]string(nil), t.Alternatives...)
		}
		if t.DelaySeconds != nil {
			d := *t.DelaySeconds
			out[i].DelaySeconds = &d
		}
		if t.OptionalProbability != nil {
			p := *t.OptionalProbability
			out[i].OptionalProbability = &p
		}
	}
	return out
}

func cloneSlots(slots map[string][]string) map[string][]string {
	if slots == nil {
		return nil
	}
	out := make(map[string][]string, len(slots))
	for k, v := range slots {
		out[k] = append([]string(nil), v...)
	}
	return out
}
