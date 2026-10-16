// Package upstream is the wsm-mcp failover pool: it holds one or more WSM
// hosts (each with its own WSM /api/v1 base URL and its own waxum base
// URL), in priority order, and runs every tool call against them with the
// failover rule from the plan:
//
//   - reads fail over on a dial error, a timeout, or a 5xx
//   - writes fail over ONLY on a dial error (the request never left, so it
//     cannot have been double-sent) — never on a timeout or a 5xx, where
//     the upstream may already have acted
//   - a 4xx never fails over (same as nginx's proxy_next_upstream)
//
// An optional per-call "target" host name pins one host and disables
// failover for that call entirely.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// Host is one WSM+waxum deployment the pool can reach.
type Host struct {
	Name         string
	WSMBaseURL   string
	WaxumBaseURL string

	httpClient *http.Client
	waxum      *waxum.Client

	poolUser, poolPass string
}

// Pool is the shared failover pool, built once at startup from env.
type Pool struct {
	Hosts []*Host

	WSMUser, WSMPass string

	AllowDangerous bool
	DefaultPhone   string

	resolver *resolver
}

// Config is Pool's env-sourced configuration. See mcp/README.md for the
// WSM_MCP_HOSTS format.
type Config struct {
	HostsSpec      string // "minipc=http://...|http://...,laptop=http://...|http://..."
	WSMUser        string
	WSMPass        string
	WaxumToken     string
	Timeout        time.Duration
	AllowDangerous bool
	DefaultPhone   string
}

// New builds a Pool from Config. Host order in HostsSpec is priority
// order: the first host listed is tried first for every call.
func New(c Config) (*Pool, error) {
	if c.HostsSpec == "" {
		return nil, errors.New("WSM_MCP_HOSTS is required (e.g. minipc=http://10.10.0.4:18801|http://10.10.0.4:3451)")
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	var hosts []*Host
	for _, entry := range strings.Split(c.HostsSpec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, urls, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("WSM_MCP_HOSTS entry %q: expected name=wsmURL|waxumURL", entry)
		}
		wsmURL, waxumURL, ok := strings.Cut(urls, "|")
		if !ok {
			return nil, fmt.Errorf("WSM_MCP_HOSTS entry %q: expected wsmURL|waxumURL", entry)
		}
		h := &Host{
			Name:         strings.TrimSpace(name),
			WSMBaseURL:   strings.TrimRight(strings.TrimSpace(wsmURL), "/"),
			WaxumBaseURL: strings.TrimRight(strings.TrimSpace(waxumURL), "/"),
			httpClient:   &http.Client{Timeout: c.Timeout},
			waxum:        waxum.New(strings.TrimRight(strings.TrimSpace(waxumURL), "/"), c.WaxumToken, c.Timeout),
			poolUser:     c.WSMUser,
			poolPass:     c.WSMPass,
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return nil, errors.New("WSM_MCP_HOSTS named no hosts")
	}
	p := &Pool{Hosts: hosts, WSMUser: c.WSMUser, WSMPass: c.WSMPass, AllowDangerous: c.AllowDangerous, DefaultPhone: c.DefaultPhone}
	p.resolver = newResolver(p, 30*time.Second, time.Now)
	return p, nil
}

// HostByName returns the named host, or nil.
func (p *Pool) HostByName(name string) *Host {
	for _, h := range p.Hosts {
		if h.Name == name {
			return h
		}
	}
	return nil
}

// OrderedHosts is the exported form of orderedHosts, for tools (like
// waxum_events_tail) that need to pick one host themselves rather than go
// through CallWSM/CallWaxum's own failover loop.
func (p *Pool) OrderedHosts(target string) ([]*Host, error) { return p.orderedHosts(target) }

// WaxumClient exposes the Host's waxum client for callers (waxum_events_tail)
// that need to call a method CallWaxum's generic reflect caller cannot
// express (a streaming callback, not a value-returning call).
func (h *Host) WaxumClient() *waxum.Client { return h.waxum }

// orderedHosts returns the hosts to try, in order, honouring an optional
// target pin. An unknown target name is an error, not a silent fallback to
// every host — a caller pinning a host expects exactly that host.
func (p *Pool) orderedHosts(target string) ([]*Host, error) {
	if target == "" {
		return p.Hosts, nil
	}
	h := p.HostByName(target)
	if h == nil {
		names := make([]string, len(p.Hosts))
		for i, h := range p.Hosts {
			names[i] = h.Name
		}
		return nil, fmt.Errorf("unknown target host %q (known: %s)", target, strings.Join(names, ", "))
	}
	return []*Host{h}, nil
}

// isDialError reports whether err means the request never reached the
// peer at all (connection refused, no route, DNS failure, TLS dial
// failure) — the only case a write is allowed to fail over on.
func isDialError(err error) bool {
	if err == nil {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// url.Error wraps the above for http.Client callers but strings.Contains
	// is kept as a fallback for a syscall.Errno that reaches here unwrapped.
	return strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no route to host")
}

// isTimeout reports a client-side timeout (ctx deadline or http.Client
// Timeout) — allowed for read failover, not for write failover.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// Attempt is one try against one host, used by callers that need to see
// per-host detail (status code, error) to decide whether to continue.
type Attempt struct {
	Host   string
	Status int
	Err    error
}

// shouldFailover applies the read/write failover rule to one attempt's
// outcome.
func shouldFailover(write bool, status int, err error) bool {
	if err != nil {
		if isDialError(err) {
			return true
		}
		if !write && isTimeout(err) {
			return true
		}
		return false
	}
	if status == 0 {
		return false
	}
	if status >= 500 {
		return !write // writes never fail over on a 5xx: it may have already acted
	}
	return false // 4xx and 2xx/3xx never fail over
}

// EnvBool parses a boolean env var, defaulting to def.
func EnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
