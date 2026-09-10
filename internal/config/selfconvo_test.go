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

// TestLoadSelfConvoDefaultsParaphraseToEnabled pins
// DefaultSelfConvoParaphrase = true: unlike SelfConvoEnabled, paraphrasing
// carries no account-safety hazard of its own — it only changes how an
// already-enabled run's text is worded — so it ships on by default.
func TestLoadSelfConvoDefaultsParaphraseToEnabled(t *testing.T) {
	cfg := &Config{}
	if err := loadSelfConvo(cfg); err != nil {
		t.Fatalf("loadSelfConvo: %v", err)
	}
	if !cfg.SelfConvoParaphrase {
		t.Error("SelfConvoParaphrase = false, want true by default")
	}
}

func TestLoadSelfConvoParaphraseParsesFalseAndRejectsAMalformedValue(t *testing.T) {
	t.Run("false", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_PARAPHRASE", "false")
		cfg := &Config{}
		if err := loadSelfConvo(cfg); err != nil {
			t.Fatalf("loadSelfConvo: %v", err)
		}
		if cfg.SelfConvoParaphrase {
			t.Error("SelfConvoParaphrase = true, want false (overridden)")
		}
	})

	t.Run("1", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_PARAPHRASE", "1")
		cfg := &Config{}
		if err := loadSelfConvo(cfg); err != nil {
			t.Fatalf("loadSelfConvo: %v", err)
		}
		if !cfg.SelfConvoParaphrase {
			t.Error(`SelfConvoParaphrase = false, want true (strconv.ParseBool("1") is true)`)
		}
	})

	t.Run("nah", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_PARAPHRASE", "nah")
		cfg := &Config{}
		err := loadSelfConvo(cfg)
		if err == nil {
			t.Fatal("loadSelfConvo with WSM_SELF_CONVO_PARAPHRASE=nah: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_SELF_CONVO_PARAPHRASE") {
			t.Fatalf("loadSelfConvo error = %q, want it to name WSM_SELF_CONVO_PARAPHRASE", err.Error())
		}
	})
}

// TestLoadSelfConvoDefaultsMaxMsgsPerDayTo200 pins
// DefaultSelfConvoMaxMsgsPerDay = 200.
func TestLoadSelfConvoDefaultsMaxMsgsPerDayTo200(t *testing.T) {
	cfg := &Config{}
	if err := loadSelfConvo(cfg); err != nil {
		t.Fatalf("loadSelfConvo: %v", err)
	}
	if cfg.SelfConvoMaxMsgsPerDay != 200 {
		t.Errorf("SelfConvoMaxMsgsPerDay = %d, want 200 by default", cfg.SelfConvoMaxMsgsPerDay)
	}
}

func TestLoadSelfConvoMaxMsgsPerDayParsesAPositiveIntegerAndRejectsAnythingElse(t *testing.T) {
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_MAX_MSGS_PER_DAY", "50")
		cfg := &Config{}
		if err := loadSelfConvo(cfg); err != nil {
			t.Fatalf("loadSelfConvo: %v", err)
		}
		if cfg.SelfConvoMaxMsgsPerDay != 50 {
			t.Errorf("SelfConvoMaxMsgsPerDay = %d, want 50 (overridden)", cfg.SelfConvoMaxMsgsPerDay)
		}
	})

	t.Run("zero is rejected", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_MAX_MSGS_PER_DAY", "0")
		cfg := &Config{}
		err := loadSelfConvo(cfg)
		if err == nil {
			t.Fatal("loadSelfConvo with WSM_SELF_CONVO_MAX_MSGS_PER_DAY=0: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_SELF_CONVO_MAX_MSGS_PER_DAY") {
			t.Fatalf("loadSelfConvo error = %q, want it to name WSM_SELF_CONVO_MAX_MSGS_PER_DAY", err.Error())
		}
	})

	t.Run("not an integer", func(t *testing.T) {
		t.Setenv("WSM_SELF_CONVO_MAX_MSGS_PER_DAY", "lots")
		cfg := &Config{}
		err := loadSelfConvo(cfg)
		if err == nil {
			t.Fatal("loadSelfConvo with WSM_SELF_CONVO_MAX_MSGS_PER_DAY=lots: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_SELF_CONVO_MAX_MSGS_PER_DAY") {
			t.Fatalf("loadSelfConvo error = %q, want it to name WSM_SELF_CONVO_MAX_MSGS_PER_DAY", err.Error())
		}
	})
}
