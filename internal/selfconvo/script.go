// Package selfconvo turns an operator-authored JSON script into a concrete,
// two-party WhatsApp conversation: Validate checks the script is well formed,
// PickPair chooses which two connected sessions play the roles, and Expand
// resolves every turn to a session, a recipient phone number and a drawn
// delay. Nothing here talks to the database or the network — that is the
// runner's job, built in a later step.
package selfconvo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	// MaxTemplateBytes caps the raw script body. Matches the 256 KiB cap
	// enforced at the HTTP layer via http.MaxBytesReader (a later step);
	// Validate re-checks it so it also holds for callers that never go
	// through the handler, such as tests and the runner's own re-validation
	// of a stored template.
	MaxTemplateBytes = 256 * 1024
	// MaxTurns is the largest script Validate accepts.
	MaxTurns = 500
	// MaxTextChars mirrors internal/handler/send.go's maxTextChars — the
	// same per-message text limit the normal send path enforces, counted in
	// runes (not bytes) so multibyte text isn't penalised for its encoding.
	MaxTextChars = 4096
	// MaxNameChars caps Script.Name.
	MaxNameChars = 120
	// MaxVarValueChars caps each value in Script.Vars, in runes.
	MaxVarValueChars = 256
	// MaxDelaySeconds bounds both Pacing.MaxSeconds and any per-turn
	// DelaySeconds override.
	MaxDelaySeconds = 3600
	// DefaultMinSeconds and DefaultMaxSeconds are used when a script omits
	// Pacing entirely.
	DefaultMinSeconds = 30
	DefaultMaxSeconds = 120
)

// ExampleFilename is the name offered on the example template's download
// response (Content-Disposition), and the stem later steps default an
// uploaded/typed name to when neither is supplied.
const ExampleFilename = "self-convo-template.json"

// Script is the top-level JSON document describing a simulated conversation.
type Script struct {
	Version int                 `json:"version"`
	Name    string              `json:"name,omitempty"`
	Pacing  *Pacing             `json:"pacing,omitempty"`
	Vars    map[string][]string `json:"vars,omitempty"`
	Turns   []Turn              `json:"turns"`
}

// Pacing is the default random-delay window a turn draws from when it does
// not set its own DelaySeconds.
type Pacing struct {
	MinSeconds int `json:"min_seconds"`
	MaxSeconds int `json:"max_seconds"`
}

// Turn is one line of the scripted exchange. Alternatives, Optional,
// OptionalProbability and any `{{var}}` placeholder in Text/Alternatives
// are the author-declared variation fields Validate checks and Vary (see
// vary.go) resolves before a run is expanded — an optional turn with no
// OptionalProbability defaults to 0.5 at Vary time, not here, so Validate
// never mutates a script based on a value it merely defaulted.
type Turn struct {
	Speaker             string   `json:"speaker"`
	Text                string   `json:"text"`
	Alternatives        []string `json:"alternatives,omitempty"`
	DelaySeconds        *int     `json:"delay_seconds,omitempty"`
	Optional            bool     `json:"optional,omitempty"`
	OptionalProbability *float64 `json:"optional_probability,omitempty"`
}

// ValidationError is every user-facing problem Validate can report. Its
// message is rendered into the UI verbatim, so it must read as something an
// operator authoring a script can act on — never a Go-internal detail.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func validationErrorf(format string, args ...any) *ValidationError {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// placeholderRE matches a `{{var_name}}` substitution placeholder, capturing
// the variable name (group 1) so callers can check it against Script.Vars
// or substitute it in place (see vary.go). Variable names are lowercase
// snake_case, matching varKeyRE below.
var placeholderRE = regexp.MustCompile(`\{\{\s*([a-z_][a-z0-9_]*)\s*\}\}`)

// varKeyRE is the allowed shape of a Script.Vars key: lowercase snake_case,
// the same shape placeholderRE requires inside `{{...}}`.
var varKeyRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Validate decodes and checks a raw JSON script. It never mutates the input
// byte slice, and the only "mutation" of the parsed result is trimming and
// normalising fields (Name, Turn.Speaker, Turn.Text) that Validate itself
// documents as normalised — every other field round-trips exactly as
// authored.
func Validate(raw []byte) (Script, error) {
	var s Script

	if len(raw) > MaxTemplateBytes {
		return s, validationErrorf("script body is %d bytes, over the %d byte limit", len(raw), MaxTemplateBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Script{}, validationErrorf("invalid JSON: %s", err)
	}
	// Reject a second JSON value appended after the first (e.g. two objects
	// concatenated). A well-formed single document decodes to io.EOF here.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Script{}, validationErrorf("script must contain exactly one JSON document")
	}

	if s.Version != 1 {
		return Script{}, validationErrorf("version must be 1, got %d", s.Version)
	}

	s.Name = strings.TrimSpace(s.Name)
	if len([]rune(s.Name)) > MaxNameChars {
		return Script{}, validationErrorf("name is over %d characters", MaxNameChars)
	}

	if len(s.Turns) < 2 {
		return Script{}, validationErrorf("script needs at least 2 turns, got %d", len(s.Turns))
	}
	if len(s.Turns) > MaxTurns {
		return Script{}, validationErrorf("script has %d turns, over the %d limit", len(s.Turns), MaxTurns)
	}

	var sawA, sawB bool
	for i := range s.Turns {
		t := &s.Turns[i]
		n := i + 1 // 1-based, for operator-facing messages

		speaker := strings.ToLower(strings.TrimSpace(t.Speaker))
		if speaker != "a" && speaker != "b" {
			return Script{}, validationErrorf("turn %d: speaker must be \"a\" or \"b\", got %q", n, t.Speaker)
		}
		t.Speaker = speaker
		if speaker == "a" {
			sawA = true
		} else {
			sawB = true
		}

		t.Text = strings.TrimSpace(t.Text)
		if t.Text == "" {
			return Script{}, validationErrorf("turn %d: text is empty", n)
		}
		if rc := len([]rune(t.Text)); rc > MaxTextChars {
			return Script{}, validationErrorf("turn %d: text is %d characters, over the %d limit", n, rc, MaxTextChars)
		}

		if t.DelaySeconds != nil {
			d := *t.DelaySeconds
			if d < 0 || d > MaxDelaySeconds {
				return Script{}, validationErrorf("turn %d: delay_seconds must be 0..%d, got %d", n, MaxDelaySeconds, d)
			}
		}

		for ai := range t.Alternatives {
			alt := strings.TrimSpace(t.Alternatives[ai])
			if alt == "" {
				return Script{}, validationErrorf("turn %d: alternatives[%d] is empty", n, ai+1)
			}
			if rc := len([]rune(alt)); rc > MaxTextChars {
				return Script{}, validationErrorf("turn %d: alternatives[%d] is %d characters, over the %d limit", n, ai+1, rc, MaxTextChars)
			}
			t.Alternatives[ai] = alt
		}

		if t.OptionalProbability != nil {
			p := *t.OptionalProbability
			if p <= 0 || p > 1 {
				return Script{}, validationErrorf("turn %d: optional_probability must be > 0 and <= 1, got %v", n, p)
			}
			if !t.Optional {
				return Script{}, validationErrorf("turn %d: optional_probability requires optional: true", n)
			}
		}
	}
	if !sawA || !sawB {
		return Script{}, validationErrorf("script must include at least one turn for each of speaker \"a\" and speaker \"b\"")
	}

	if s.Pacing == nil {
		s.Pacing = &Pacing{MinSeconds: DefaultMinSeconds, MaxSeconds: DefaultMaxSeconds}
	} else {
		p := s.Pacing
		if p.MinSeconds < 1 || p.MinSeconds > p.MaxSeconds || p.MaxSeconds > MaxDelaySeconds {
			return Script{}, validationErrorf("pacing must satisfy 1 <= min_seconds <= max_seconds <= %d, got min=%d max=%d", MaxDelaySeconds, p.MinSeconds, p.MaxSeconds)
		}
	}

	if err := validateVars(s.Vars); err != nil {
		return Script{}, err
	}
	for i := range s.Turns {
		t := &s.Turns[i]
		n := i + 1
		if err := checkPlaceholdersHaveVars(t.Text, s.Vars, n); err != nil {
			return Script{}, err
		}
		for _, alt := range t.Alternatives {
			if err := checkPlaceholdersHaveVars(alt, s.Vars, n); err != nil {
				return Script{}, err
			}
		}
	}

	return s, nil
}

// validateVars checks Script.Vars: each key must be lowercase snake_case
// (varKeyRE, the same shape a `{{placeholder}}` requires), have at least one
// value, and every value must be non-empty after trimming and at most
// MaxVarValueChars runes. Values are trimmed in place — the map's slices are
// mutated directly, matching Validate's documented normalisation of other
// fields (Name, Turn.Speaker, Turn.Text).
func validateVars(vars map[string][]string) error {
	for key, values := range vars {
		if !varKeyRE.MatchString(key) {
			return validationErrorf("vars: key %q must be lowercase snake_case (match %s)", key, varKeyRE.String())
		}
		if len(values) == 0 {
			return validationErrorf("vars: key %q must have at least one value", key)
		}
		for i, v := range values {
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return validationErrorf("vars: key %q value %d is empty", key, i+1)
			}
			if rc := len([]rune(trimmed)); rc > MaxVarValueChars {
				return validationErrorf("vars: key %q value %d is %d characters, over the %d limit", key, i+1, rc, MaxVarValueChars)
			}
			values[i] = trimmed
		}
	}
	return nil
}

// checkPlaceholdersHaveVars scans text for every `{{key}}` placeholder and
// fails, naming turn n and the offending key, if that key has no entry in
// vars. Called against both a turn's Text and each of its Alternatives,
// since either can be the one a run actually sends (see Vary).
func checkPlaceholdersHaveVars(text string, vars map[string][]string, n int) error {
	for _, m := range placeholderRE.FindAllStringSubmatch(text, -1) {
		key := m[1]
		if _, ok := vars[key]; !ok {
			return validationErrorf("turn %d: placeholder {{%s}} has no matching entry in vars", n, key)
		}
	}
	return nil
}
