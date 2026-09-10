package corpus

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed packs/*.json
var packFiles embed.FS

// Packs decodes and validates every embedded pack, sorted by Topic — a
// fixed order Generate relies on for determinism (see generate.go). It
// fails loudly on a malformed pack or a structural problem Validate
// catches, and on two packs sharing a Topic (Generate keys everything by
// topic, so a collision would silently merge two topics' templates).
func Packs() ([]Pack, error) {
	entries, err := packFiles.ReadDir("packs")
	if err != nil {
		return nil, fmt.Errorf("corpus: read embedded packs dir: %w", err)
	}
	packs := make([]Pack, 0, len(entries))
	seenTopic := map[string]string{} // topic -> file that claimed it first
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := packFiles.ReadFile("packs/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("corpus: read %s: %w", e.Name(), err)
		}
		var p Pack
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("corpus: decode %s: %w", e.Name(), err)
		}
		if err := Validate(p); err != nil {
			return nil, fmt.Errorf("corpus: %s: %w", e.Name(), err)
		}
		if other, ok := seenTopic[p.Topic]; ok {
			return nil, fmt.Errorf("corpus: topic %q declared in both %s and %s", p.Topic, other, e.Name())
		}
		seenTopic[p.Topic] = e.Name()
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].Topic < packs[j].Topic })
	return packs, nil
}
