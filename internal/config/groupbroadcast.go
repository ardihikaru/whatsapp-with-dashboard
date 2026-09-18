package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// DefaultGroupBroadcastEnabled is FALSE — same "automated outbound traffic
// needs explicit opt-in" reasoning as DefaultSelfConvoEnabled/
// DefaultCallsEnabled: a group-broadcast schedule has one of this
// deployment's own paired sessions post into a real WhatsApp group, and a
// configurable list of others reply into it, automatically and on a
// recurring cadence — exactly the bursty, scripted, device-to-device
// traffic pattern that risks a WhatsApp ban, and none of the paired
// identities involved has a second copy anywhere to fall back on (see
// CLAUDE.md's whatsmeow/waxum singleton-volume hazards). An operator opts
// in deliberately.
const DefaultGroupBroadcastEnabled = false

// DefaultGroupBroadcastMinSessionGap is the minimum gap PlanDay enforces
// between any two sends FROM THE SAME session (sender or receiver, opener
// or reply) on the same date, across every group-broadcast schedule — see
// the phase plan's confirmed decision #9. 25s is comfortably above WhatsApp
// message round-trip jitter while still reading as "two separate human
// replies", not the obviously-automated shape of two sends a second apart.
const DefaultGroupBroadcastMinSessionGap = 25 * time.Second

// DefaultGroupBroadcastAIDeadline bounds one aiclient.GenerateMessage call
// during planning — ai-codex-cli is an async job runner, not a synchronous
// paraphrase endpoint, so a bound exists to guarantee a slow/stuck job never
// stalls the whole planning pass; internal/groupbroadcast's Planner falls
// back to the local paraphrase engine once this expires.
const DefaultGroupBroadcastAIDeadline = 90 * time.Second

// loadGroupBroadcast resolves WSM_GROUP_BROADCAST_ENABLED,
// WSM_GROUP_BROADCAST_MIN_SESSION_GAP and WSM_GROUP_BROADCAST_AI_DEADLINE —
// follows loadSelfConvo's shape exactly: unset resolves to the default, an
// unparseable value is a boot-time error naming the variable and the raw
// value, never a silent fallback. Must run BEFORE loadAIClient: that
// function's WSM_CODEX_CLI_* requiredness is conditional on
// cfg.GroupBroadcastEnabled already being resolved.
func loadGroupBroadcast(cfg *Config) error {
	cfg.GroupBroadcastEnabled = DefaultGroupBroadcastEnabled
	if raw := os.Getenv("WSM_GROUP_BROADCAST_ENABLED"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_GROUP_BROADCAST_ENABLED %q: %v", raw, err)
		}
		cfg.GroupBroadcastEnabled = b
	}

	cfg.GroupBroadcastMinSessionGap = DefaultGroupBroadcastMinSessionGap
	if raw := os.Getenv("WSM_GROUP_BROADCAST_MIN_SESSION_GAP"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_GROUP_BROADCAST_MIN_SESSION_GAP %q: %v", raw, err)
		}
		cfg.GroupBroadcastMinSessionGap = d
	}

	cfg.GroupBroadcastAIDeadline = DefaultGroupBroadcastAIDeadline
	if raw := os.Getenv("WSM_GROUP_BROADCAST_AI_DEADLINE"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_GROUP_BROADCAST_AI_DEADLINE %q: %v", raw, err)
		}
		cfg.GroupBroadcastAIDeadline = d
	}
	return nil
}
