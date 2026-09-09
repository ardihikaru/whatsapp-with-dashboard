package selfconvo

import _ "embed"

// ExampleJSON is the shipped example script, offered as a downloadable
// template and validated by TestValidateAcceptsTheEmbeddedExample so it can
// never regress against Validate's own rules.
//
//go:embed example.json
var ExampleJSON []byte
