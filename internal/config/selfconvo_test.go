package config

import (
	"strings"
	"testing"
)

// TestLoadSelfConvoDefaultsToDisabled pins DefaultSelfConvoEnabled = false:
// two of this deployment's own paired sessions chatting automatically is
// exactly the traffic pattern that gets a WhatsApp identity flagged, and
// neither identity has a second copy anywhere (see CLAUDE.md's
// singleton-volume entries), so the honest default is disarmed, same
// pattern as WSM_CALLS_ENABLED.
func TestLoadSelfConvoDefaultsToDisabled(t *testing.T) {
	cfg := &Config{}
	if err := loadSelfConvo(cfg); err != nil {
		t.Fatalf("loadSelfConvo: %v", err)
	}
	if cfg.SelfConvoEnabled {
		t.Error("SelfConvoEnabled = true, want false by default")
	}
}

func TestLoadSelfConvoParsesTrueAndRejectsAMalformedValue(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_ENABLED", "true")
		cfg := &Config{}
		if err := loadSelfConvo(cfg); err != nil {
			t.Fatalf("loadSelfConvo: %v", err)
		}
		if !cfg.SelfConvoEnabled {
			t.Error("SelfConvoEnabled = false, want true (overridden)")
		}
	})

	t.Run("0", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_ENABLED", "0")
		cfg := &Config{}
		if err := loadSelfConvo(cfg); err != nil {
			t.Fatalf("loadSelfConvo: %v", err)
		}
		if cfg.SelfConvoEnabled {
			t.Error(`SelfConvoEnabled = true, want false (strconv.ParseBool("0") is false)`)
		}
	})

	t.Run("yes", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_ENABLED", "yes")
		cfg := &Config{}
		err := loadSelfConvo(cfg)
		if err == nil {
			t.Fatal("loadSelfConvo with WSM_SELF_CONVO_ENABLED=yes: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_SELF_CONVO_ENABLED") {
			t.Fatalf("loadSelfConvo error = %q, want it to name WSM_SELF_CONVO_ENABLED", err.Error())
		}
	})
}
