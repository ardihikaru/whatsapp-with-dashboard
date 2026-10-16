// Package version embeds mcp/VERSION so cmd/wsm-mcp and both front doors
// can report the running build's version without a build-time ldflags
// dance. Bump mcp/VERSION whenever a tool is added or changed — see
// CLAUDE.md's "wsm-mcp must track every WSM and waxum change" section.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version is mcp/VERSION's contents, trimmed of surrounding whitespace.
func Version() string { return strings.TrimSpace(raw) }
