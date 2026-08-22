package config

import (
	"net/netip"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"single cidr", "10.10.0.0/24", []string{"10.10.0.0/24"}},
		// A bare address is the form most operators reach for first.
		{"bare address", "10.10.0.1", []string{"10.10.0.1/32"}},
		{"mixed list with spaces", "10.10.0.0/24, 127.0.0.1", []string{"10.10.0.0/24", "127.0.0.1/32"}},
		{"ipv6", "2001:db8::/32", []string{"2001:db8::/32"}},
		{"trailing comma", "10.10.0.0/24,", []string{"10.10.0.0/24"}},
		// 10.10.0.1/24 is a typo for the network address; an unmasked prefix
		// makes Contains behave unpredictably, so it is normalised.
		{"host bits are masked off", "10.10.0.1/24", []string{"10.10.0.0/24"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTrustedProxies(tc.raw)
			if err != nil {
				t.Fatalf("parseTrustedProxies(%q) error: %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i, w := range tc.want {
				if got[i] != netip.MustParsePrefix(w) {
					t.Errorf("prefix %d = %v, want %v", i, got[i], w)
				}
			}
		})
	}
}

// A malformed entry must fail the boot, not be skipped: dropping one would
// leave the service running with a narrower trust set than was written.
func TestParseTrustedProxiesRejects(t *testing.T) {
	for _, raw := range []string{"nonsense", "10.10.0.0/33", "10.10.0.0/24,nonsense", " , ", "10.10.0.256"} {
		if _, err := parseTrustedProxies(raw); err == nil {
			t.Errorf("parseTrustedProxies(%q) = nil error, want a failure", raw)
		}
	}
}
