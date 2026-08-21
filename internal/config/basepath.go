package config

import (
	"fmt"
	"net/url"
	"strings"
)

// basePath extracts the mount prefix from a base URL.
//
// Rejecting a query or fragment is not pedantry: this prefix is concatenated
// into every route pattern and every {{u}} template call, so a BaseURL of
// "https://example.test/wsm?x=1" would register patterns like "/wsm?x=1/login" —
// a mount that registers cleanly and then matches no request anyone can make.
// Fail at boot instead, where someone is watching.
func basePath(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("WA_BASE_URL is not a URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("WA_BASE_URL must be absolute (scheme://host[/path]), got %q", base)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("WA_BASE_URL must not carry a query or fragment, got %q", base)
	}
	// A trailing slash is the same mount as none, but it would double up on
	// every concatenation. Normalise it away rather than tolerate it.
	return strings.TrimSuffix(u.Path, "/"), nil
}
