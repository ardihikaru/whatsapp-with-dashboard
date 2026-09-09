// Package id is the one leaf function that mints this app's random
// identifiers (sessions, messages, self-convo templates/runs/turns' message
// ids, ...). It imports nothing from this repo, on purpose: internal/wa.NewID
// used to own this body directly, and internal/selfconvo/runner.go
// duplicated it byte for byte rather than import package wa (runner.go's old
// doc comment explained why: it wanted to keep that import confined to
// gateway_wa.go alone). Both now call here instead, so there is exactly one
// implementation to keep in sync.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a 16-byte crypto/rand hex identifier — 32 lowercase hex
// characters, collision-safe for this app's purposes (sessions, messages,
// self-convo rows, ...).
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}
