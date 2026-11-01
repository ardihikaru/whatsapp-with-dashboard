package wa

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

var groupCodeShape = regexp.MustCompile(`^[a-z0-9_-]+$`)

const groupCodeMaxLen = 32

// NormalizeGroupCode validates and canonicalizes a routing group code:
// trimmed, lowercased, "" (ungrouped — ALWAYS valid, never an error, since
// that is every existing session's default), else at most 32 characters
// matching [a-z0-9_-].
//
// This lives in internal/wa rather than internal/handler, unlike most
// input validation in this codebase, because it has to serve more than one
// layer: four handler call sites (create-session, PATCH/route params on the
// send endpoints), the session-edit UI form, AND ManagedSession.SetGroupCode
// itself (session.go) — which must validate before ever touching state or
// the DB. internal/handler already imports internal/wa (see NormalizePhone),
// so putting the single writer's own validator here, rather than duplicating
// it in handler and importing it back, keeps one authority instead of two.
func NormalizeGroupCode(raw string) (string, error) {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" {
		return "", nil
	}
	if len(code) > groupCodeMaxLen {
		return "", errors.New("group_code must be 32 characters or fewer")
	}
	if !groupCodeShape.MatchString(code) {
		return "", errors.New(`group_code may contain only lowercase letters, digits, "-" and "_"`)
	}
	return code, nil
}

// NormalizeGroupCodes validates and canonicalizes a whole routing-group
// SET: each code through NormalizeGroupCode, "" entries dropped, duplicates
// dropped, the result sorted — the exact pure logic
// ManagedSession.SetGroupCodes applies before ever touching state or the
// DB, exported so a caller that must validate several fields TOGETHER
// before writing any of them (the /sessions edit dialog's PostSessionEdit,
// internal/handler) can check this one without a side effect, mirroring
// ValidateSessionName (session.go) for the name half of that same form.
// Returns the first invalid code's error immediately — no partial result.
func NormalizeGroupCodes(raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		code, err := NormalizeGroupCode(r)
		if err != nil {
			return nil, err
		}
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Strings(out)
	return out, nil
}
