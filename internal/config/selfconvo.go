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

// loadSelfConvo resolves WSM_SELF_CONVO_ENABLED, gating internal/selfconvo's
// runner (wired in cmd/app/main.go). Follows loadMedia's WSM_CALLS_ENABLED
// block (media.go:141-148) exactly: unset resolves to the default, an
// unparseable value is a boot-time error naming the variable and the raw
// value, never a silent fallback.
func loadSelfConvo(cfg *Config) error {
	cfg.SelfConvoEnabled = DefaultSelfConvoEnabled
	if raw := os.Getenv("WSM_SELF_CONVO_ENABLED"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_SELF_CONVO_ENABLED %q: %v", raw, err)
		}
		cfg.SelfConvoEnabled = b
	}
	return nil
}
