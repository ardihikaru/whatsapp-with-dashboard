package paraphrase

import (
	_ "embed"
	"encoding/json"
	"math/rand/v2"
)

//go:embed emoji.json
var emojiJSON []byte

// emojiPools maps a topic ("" is the neutral pool) to its emoji choices,
// populated once at package init from the embedded JSON.
var emojiPools map[string][]string

func init() {
	if err := json.Unmarshal(emojiJSON, &emojiPools); err != nil {
		emojiPools = map[string][]string{}
	}
}

// applyEmoji appends, at most, a single trailing emoji drawn from topic's
// pool (falling back to the neutral "" pool when topic is unknown or
// empty), gated by rate. It never inserts mid-sentence and never fires
// when rate <= 0 — the documented default-off behaviour of
// Options.EmojiRate.
func applyEmoji(text string, topic string, rate float64, rng *rand.Rand) string {
	if rate <= 0 {
		return text
	}
	if rng.Float64() >= rate {
		return text
	}
	pool := emojiPools[topic]
	if len(pool) == 0 {
		pool = emojiPools[""]
	}
	if len(pool) == 0 {
		return text
	}
	return text + " " + pool[rng.IntN(len(pool))]
}
