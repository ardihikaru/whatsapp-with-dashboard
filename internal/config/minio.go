package config

import (
	"fmt"
	"net/url"
)

// parseMinioEndpoint splits a WSM_MINIO_ENDPOINT URL into the bare
// host[:port] minio-go's client wants and a secure flag from the scheme.
// Rejecting anything but http/https with a host, at boot, matches basePath's
// treatment of WA_BASE_URL — the alternative is a client that "connects"
// successfully and then fails every request with an unhelpful transport error.
func parseMinioEndpoint(raw string) (host string, secure bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("WSM_MINIO_ENDPOINT is not a URL: %w", err)
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("WSM_MINIO_ENDPOINT must be absolute (scheme://host), got %q", raw)
	}
	switch u.Scheme {
	case "https":
		secure = true
	case "http":
		secure = false
	default:
		return "", false, fmt.Errorf("WSM_MINIO_ENDPOINT scheme must be http or https, got %q", raw)
	}
	return u.Host, secure, nil
}
