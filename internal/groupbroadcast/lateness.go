package groupbroadcast

import (
	"math/rand/v2"
	"time"
)

// lateReasons is the local reason bank a late receiver's excuse is drawn
// from (confirmed decision #4: "still paraphrased by AI for daily wording
// variety" — see wording.go's promptForTurn, which folds the drawn reason
// key into the AI prompt, and the local fallback templates, which render it
// directly). Keys are short, ASCII, stable identifiers stored in
// wa_group_broadcast_turns.late_reason — NOT the rendered sentence itself,
// so the operator-visible turn table can show a stable reason even when the
// exact wording differs every day.
var lateReasons = []string{
	"macet",
	"ban_bocor",
	"anak_sakit",
	"hujan_deras",
	"antre_bensin",
	"kesiangan",
	"internet_lemot",
}

// drawLateReason picks one reason key uniformly at random.
func drawLateReason(rng *rand.Rand) string {
	return lateReasons[rng.IntN(len(lateReasons))]
}

// isLate reports whether at, once converted to loc, falls AFTER cutoff
// (parsed as "HH:MM" on at's own local date) — `.After`, deliberately not
// `.After`-or-equal, so a draw landing EXACTLY on the cutoff second is NOT
// late (see the phase plan's explicit boundary requirement: 08:30:00 is not
// late, 08:30:01 is).
func isLate(at time.Time, loc *time.Location, cutoff clockTime) bool {
	local := at.In(loc)
	y, m, d := local.Date()
	cutoffAt := time.Date(y, m, d, cutoff.hour, cutoff.min, 0, 0, loc)
	return local.After(cutoffAt)
}
