package config

import (
	"strings"
	"testing"
)

// TestLoadHostLabelDefaultsToEmptyWhenUnset pins DefaultHostLabel = "": an
// operator who has not opted in sees no chip at all (platformLink's "" hides
// the link" convention, reused here — see loadHostLabel's doc comment).
func TestLoadHostLabelDefaultsToEmptyWhenUnset(t *testing.T) {
	cfg := &Config{}
	if err := loadHostLabel(cfg); err != nil {
		t.Fatalf("loadHostLabel: %v", err)
	}
	if cfg.HostLabel != "" {
		t.Errorf("HostLabel = %q, want \"\" when WSM_HOST_LABEL is unset", cfg.HostLabel)
	}
}

// TestLoadHostLabelTreatsAnExplicitEmptyValueAsMeaningful mirrors
// WA_PLATFORM_URL's LookupEnv convention: WSM_HOST_LABEL="" must resolve the
// same as unset (both hide the chip), proving the code path reads the
// env var at all rather than merely defaulting.
func TestLoadHostLabelTreatsAnExplicitEmptyValueAsMeaningful(t *testing.T) {
	t.Setenv("WSM_HOST_LABEL", "")
	cfg := &Config{}
	if err := loadHostLabel(cfg); err != nil {
		t.Fatalf("loadHostLabel: %v", err)
	}
	if cfg.HostLabel != "" {
		t.Errorf("HostLabel = %q, want \"\"", cfg.HostLabel)
	}
}

// TestLoadHostLabelReadsAnExplicitValue is the test that would fail if
// loadHostLabel stopped reading WSM_HOST_LABEL at all.
func TestLoadHostLabelReadsAnExplicitValue(t *testing.T) {
	t.Setenv("WSM_HOST_LABEL", "MINIPC")
	cfg := &Config{}
	if err := loadHostLabel(cfg); err != nil {
		t.Fatalf("loadHostLabel: %v", err)
	}
	if cfg.HostLabel != "MINIPC" {
		t.Errorf("HostLabel = %q, want %q", cfg.HostLabel, "MINIPC")
	}
}

// TestLoadHostLabelDoesNotHardcodeTheTwoKnownValues: the config must accept a
// third machine's own name without a code change — MINIPC/LAPTOP are just
// what this deployment happens to use today, never a validated set.
func TestLoadHostLabelDoesNotHardcodeTheTwoKnownValues(t *testing.T) {
	t.Setenv("WSM_HOST_LABEL", "DR-SITE-3")
	cfg := &Config{}
	if err := loadHostLabel(cfg); err != nil {
		t.Fatalf("loadHostLabel: %v", err)
	}
	if cfg.HostLabel != "DR-SITE-3" {
		t.Errorf("HostLabel = %q, want %q", cfg.HostLabel, "DR-SITE-3")
	}
}

// TestLoadHostLabelRejectsAValueLongerThanTheCap proves MaxHostLabelLen is
// actually enforced, not just documented.
func TestLoadHostLabelRejectsAValueLongerThanTheCap(t *testing.T) {
	t.Setenv("WSM_HOST_LABEL", strings.Repeat("x", MaxHostLabelLen+1))
	cfg := &Config{}
	err := loadHostLabel(cfg)
	if err == nil {
		t.Fatal("loadHostLabel with an over-long WSM_HOST_LABEL: got nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "WSM_HOST_LABEL") {
		t.Errorf("loadHostLabel error = %q, want it to name WSM_HOST_LABEL", err.Error())
	}
}

// TestLoadHostLabelAcceptsAValueExactlyAtTheCap is the off-by-one companion
// to the rejection test above.
func TestLoadHostLabelAcceptsAValueExactlyAtTheCap(t *testing.T) {
	v := strings.Repeat("x", MaxHostLabelLen)
	t.Setenv("WSM_HOST_LABEL", v)
	cfg := &Config{}
	if err := loadHostLabel(cfg); err != nil {
		t.Fatalf("loadHostLabel at the cap: %v", err)
	}
	if cfg.HostLabel != v {
		t.Errorf("HostLabel = %q, want the full %d-byte value", cfg.HostLabel, MaxHostLabelLen)
	}
}
