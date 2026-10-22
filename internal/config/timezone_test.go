package config

import (
	"strings"
	"testing"
)

// TestLoadTimezoneDefaultsToAsiaJakartaWhenUnset is the test that goes red
// if loadTimezone stops defaulting (e.g. falls back to UTC or time.Local
// instead) — DefaultTimezone names the zone explicitly so it cannot drift
// with the host's own /etc/localtime.
func TestLoadTimezoneDefaultsToAsiaJakartaWhenUnset(t *testing.T) {
	cfg := &Config{}
	if err := loadTimezone(cfg); err != nil {
		t.Fatalf("loadTimezone: %v", err)
	}
	if cfg.Location == nil {
		t.Fatal("Location is nil, want a resolved *time.Location")
	}
	if cfg.Location.String() != DefaultTimezone {
		t.Errorf("Location = %q, want %q", cfg.Location.String(), DefaultTimezone)
	}
}

// TestLoadTimezoneReadsAnExplicitOverride proves WSM_TIMEZONE is actually
// consulted, not merely defaulted.
func TestLoadTimezoneReadsAnExplicitOverride(t *testing.T) {
	t.Setenv("WSM_TIMEZONE", "America/New_York")
	cfg := &Config{}
	if err := loadTimezone(cfg); err != nil {
		t.Fatalf("loadTimezone: %v", err)
	}
	if cfg.Location.String() != "America/New_York" {
		t.Errorf("Location = %q, want %q", cfg.Location.String(), "America/New_York")
	}
}

// TestLoadTimezoneRejectsAnUnknownZoneName is the fail-fast half: a typo'd
// WSM_TIMEZONE must boot-fail loudly rather than silently serve the wrong
// (or a zero-value) location to every page.
func TestLoadTimezoneRejectsAnUnknownZoneName(t *testing.T) {
	t.Setenv("WSM_TIMEZONE", "Not/AZone")
	cfg := &Config{}
	err := loadTimezone(cfg)
	if err == nil {
		t.Fatal("loadTimezone with an unknown zone: got nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "WSM_TIMEZONE") {
		t.Errorf("loadTimezone error = %q, want it to name WSM_TIMEZONE", err.Error())
	}
}

// TestLoadTimezoneTreatsAnExplicitEmptyValueAsUnset is the one deliberate
// divergence from loadHostLabel/WA_PLATFORM_URL's LookupEnv convention:
// there is no "hide the timezone" concept, so WSM_TIMEZONE="" must resolve
// to DefaultTimezone exactly like unset, not to some other meaning.
func TestLoadTimezoneTreatsAnExplicitEmptyValueAsUnset(t *testing.T) {
	t.Setenv("WSM_TIMEZONE", "")
	cfg := &Config{}
	if err := loadTimezone(cfg); err != nil {
		t.Fatalf("loadTimezone: %v", err)
	}
	if cfg.Location.String() != DefaultTimezone {
		t.Errorf("Location = %q, want %q", cfg.Location.String(), DefaultTimezone)
	}
}
