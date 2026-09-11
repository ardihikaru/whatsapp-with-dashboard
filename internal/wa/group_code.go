package wa

import (
	"errors"
	"regexp"
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
