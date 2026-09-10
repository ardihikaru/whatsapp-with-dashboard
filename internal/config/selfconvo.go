package config

import (
	"fmt"
	"os"
	"strconv"
)

// DefaultSelfConvoEnabled is FALSE — same reasoning as DefaultCallsEnabled
// (media.go), a different hazard. A self-convo run has two of this
// deployment's own PAIRED sessions exchange a scripted conversation
// automatically; that is precisely the traffic pattern (bursty, scripted,
// device-to-device, no human in the loop) that gets a WhatsApp account
// flagged, and neither paired identity has a second copy anywhere to fall
// back on if one of them gets banned for it — see CLAUDE.md's whatsmeow and
// waxum singleton-volume hazard entries. Shipping the capability disarmed by
// default is the honest choice; an operator opts in deliberately.
const DefaultSelfConvoEnabled = false

// DefaultSelfConvoParaphrase is TRUE: once an operator has opted into
// self-convo at all (DefaultSelfConvoEnabled above), a scripted run should
// read like a different way of saying the same thing each time by default,
// not the exact same bytes on every run — see
// internal/selfconvo/paraphrase's package doc for what "paraphrase" means
// here (a bounded, register-locked SURFACE rewrite, not a semantic one).
// Unlike SelfConvoEnabled, this flag carries no account-safety hazard of
// its own; it only changes HOW an already-enabled run's text is worded.
const DefaultSelfConvoParaphrase = true

// DefaultSelfConvoMaxMsgsPerDay bounds how many turns internal/selfconvo's
// Planner will schedule FROM one sender session in one day — see
// selfconvo.PlannerOptions.MaxMsgsPerDay's own doc comment for what happens
// at the cap (planning simply stops for that sender; nothing is stalled).
// 200 is generous enough for a handful of daily conversations across
// several receivers while still bounding the account-safety exposure
// DefaultSelfConvoEnabled's own doc comment describes — an unbounded
// scheduler is a worse version of the same hazard a manual Trigger already
// carries, just automated.
const DefaultSelfConvoMaxMsgsPerDay = 200

// loadSelfConvo resolves WSM_SELF_CONVO_ENABLED, WSM_SELF_CONVO_PARAPHRASE
// and WSM_SELF_CONVO_MAX_MSGS_PER_DAY, gating internal/selfconvo's runner
// and planner (wired in cmd/app/main.go via cmd/app/selfconvo.go). Follows
// loadMedia's WSM_CALLS_ENABLED block (media.go:141-148) exactly for each:
// unset resolves to the default, an unparseable value is a boot-time error
// naming the variable and the raw value, never a silent fallback.
func loadSelfConvo(cfg *Config) error {
	cfg.SelfConvoEnabled = DefaultSelfConvoEnabled
	if raw := os.Getenv("WSM_SELF_CONVO_ENABLED"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_SELF_CONVO_ENABLED %q: %v", raw, err)
		}
		cfg.SelfConvoEnabled = b
	}

	cfg.SelfConvoParaphrase = DefaultSelfConvoParaphrase
	if raw := os.Getenv("WSM_SELF_CONVO_PARAPHRASE"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_SELF_CONVO_PARAPHRASE %q: %v", raw, err)
		}
		cfg.SelfConvoParaphrase = b
	}

	cfg.SelfConvoMaxMsgsPerDay = DefaultSelfConvoMaxMsgsPerDay
	if raw := os.Getenv("WSM_SELF_CONVO_MAX_MSGS_PER_DAY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid WSM_SELF_CONVO_MAX_MSGS_PER_DAY %q: must be a positive integer", raw)
		}
		cfg.SelfConvoMaxMsgsPerDay = n
	}
	return nil
}
