package wa

import "testing"

// TestNormalizeGroupCodeAcceptsOnlyTheDocumentedShape pins the exact
// contract SetGroupCode and every future handler call site depend on:
// trimmed, lowercased, "" is always valid (the ungrouped default every
// existing session must keep working with), and anything else must match
// [a-z0-9_-] within 32 characters.
func TestNormalizeGroupCodeAcceptsOnlyTheDocumentedShape(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "trims and lowercases", in: " Alpha ", want: "alpha"},
		{name: "digits, hyphen and underscore are allowed", in: "a_b-1", want: "a_b-1"},
		{name: "empty string is the ungrouped default, never an error", in: "", want: ""},
		{name: "spaces inside the code are rejected", in: "kemang selatan", wantErr: true},
		{name: "non-ascii is rejected", in: "Ünicode", wantErr: true},
		{name: "exactly 32 chars is allowed", in: "abcdefghijklmnopqrstuvwxyz012345", want: "abcdefghijklmnopqrstuvwxyz012345"},
		{name: "33 chars is rejected", in: "abcdefghijklmnopqrstuvwxyz0123456", wantErr: true},
		{name: "uppercase alone still normalizes, not an error", in: "UPPER", want: "upper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeGroupCode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeGroupCode(%q) = %q, nil; want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeGroupCode(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeGroupCode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
