package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// prefixes is the parse step config.Load performs, inlined so these cases can
// be read without cross-referencing the config package.
func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("bad test fixture %q: %v", c, err)
		}
		out = append(out, p.Masked())
	}
	return out
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		trusted []string
		remote  string
		xff     []string
		want    string
	}{
		{
			// The default, and the regression guard that matters most: with no
			// proxy named the header is not evidence of anything.
			name:   "no trusted proxies ignores the header",
			remote: "203.0.113.9:5555",
			xff:    []string{"198.51.100.1"},
			want:   "203.0.113.9",
		},
		{
			name:    "trusted peer, single hop",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"198.51.100.1"},
			want:    "198.51.100.1",
		},
		{
			// The attack: the caller prepends whatever it likes and nginx
			// appends the real address to the RIGHT of it. Taking the leftmost
			// entry would hand out unlimited throttle keys.
			name:    "trusted peer, spoofed entries on the left are ignored",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"1.2.3.4, 5.6.7.8, 198.51.100.1"},
			want:    "198.51.100.1",
		},
		{
			name:    "trusted peer, chain of trusted hops falls back to the peer",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"10.10.0.5, 10.10.0.1"},
			want:    "10.10.0.1",
		},
		{
			name:    "trusted peer, trusted hop then real client",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"198.51.100.1, 10.10.0.5"},
			want:    "198.51.100.1",
		},
		{
			name:    "untrusted peer, header ignored",
			trusted: []string{"10.10.0.0/24"},
			remote:  "203.0.113.9:5555",
			xff:     []string{"198.51.100.1"},
			want:    "203.0.113.9",
		},
		{
			name:    "trusted peer, no header",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			want:    "10.10.0.1",
		},
		{
			// A repeated header is legal and reads as one chain in order, so
			// the rightmost hop is the last entry of the last value.
			name:    "trusted peer, header repeated",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"1.2.3.4", "198.51.100.1, 10.10.0.5"},
			want:    "198.51.100.1",
		},
		{
			// Stop at the garbage rather than skipping past it: everything to
			// its left is no longer vouched for by anything.
			name:    "trusted peer, malformed hop stops the walk",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"198.51.100.1, not-an-ip"},
			want:    "10.10.0.1",
		},
		{
			name:    "trusted peer, empty header value",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{""},
			want:    "10.10.0.1",
		},
		{
			// Dual-stack listeners report an IPv4 peer in v6-mapped form; an
			// IPv4 prefix must still contain it.
			name:    "v6-mapped peer matches an ipv4 prefix",
			trusted: []string{"10.10.0.0/24"},
			remote:  "[::ffff:10.10.0.1]:41000",
			xff:     []string{"198.51.100.1"},
			want:    "198.51.100.1",
		},
		{
			name:    "ipv6 client through a trusted proxy",
			trusted: []string{"10.10.0.0/24"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"2001:db8::1"},
			want:    "2001:db8::1",
		},
		{
			name:    "single-host prefix",
			trusted: []string{"10.10.0.1/32"},
			remote:  "10.10.0.1:41000",
			xff:     []string{"198.51.100.1"},
			want:    "198.51.100.1",
		},
		{
			name:    "trusted set does not cover the peer",
			trusted: []string{"10.10.0.1/32"},
			remote:  "10.10.0.2:41000",
			xff:     []string{"198.51.100.1"},
			want:    "10.10.0.2",
		},
		{
			// httptest and some listeners hand over a bare address.
			name:   "RemoteAddr without a port",
			remote: "203.0.113.9",
			want:   "203.0.113.9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetTrustedProxies(prefixes(t, tc.trusted...))
			t.Cleanup(func() { SetTrustedProxies(nil) })

			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ClientIP(r); got != tc.want {
				t.Errorf("ClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// SetTrustedProxies must not alias the caller's slice: config owns that, and a
// later append there would silently widen the trust set.
func TestSetTrustedProxiesCopies(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxies(nil) })

	src := prefixes(t, "10.10.0.0/24")
	SetTrustedProxies(src)
	src[0] = netip.MustParsePrefix("0.0.0.0/0")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("ClientIP() = %q, want the peer — the stored set was aliased", got)
	}
}
