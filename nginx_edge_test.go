package wsm_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// docs/deploy-nginx-wa.conf.example is the reviewable mirror of the live file at
// /etc/nginx/conf.d/wa.conf on the public edge box (`ssh clino`). Nothing in
// this program reads it, so these tests go to disk the same way the compose
// tests do; `go test` runs a package in its own directory, and this package is
// the repo root.
//
// ── WHY AN EDGE CONFIG HAS TESTS AT ALL ─────────────────────────────────────
//
// The edge is the ONLY thing standing between the public internet and a JSON
// API that sends WhatsApp messages from a device identity with no second copy
// anywhere. It publishes a deliberate allowlist; every other route under
// /api/v1 answers 404 before it is ever proxied. Two of the ways that
// arrangement breaks are silent — nginx starts, reloads, and serves happily
// while the guarantee is gone — so they are asserted here rather than left to
// be noticed in production:
//
//  1. An allow rule appended AFTER the deny regex is dead code (nginx takes
//     the first matching regex), so the route it meant to publish stays 404
//     and the author sees only a config that reloaded cleanly.
//  2. A proxy_set_header inside a location silently discards the whole
//     inherited set for that location, taking X-Forwarded-For with it. See
//     TestTheEdgeSetsProxyHeadersAtServerLevelSoAPILocationsInheritThem.
const edgeConf = "docs/deploy-nginx-wa.conf.example"

func edgeSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(edgeConf)
	if err != nil {
		t.Fatalf("read %s: %v", edgeConf, err)
	}
	return string(b)
}

// edgeLocation is one `location <match> { … }` directive: the matcher exactly as
// written (modifier included, e.g. `= /api/v1/messages` or `~ ^/api/v1/`) and
// the body between its braces.
type edgeLocation struct {
	match string
	body  string
}

// edgeLocations scans the config into its location blocks IN FILE ORDER, which
// is the whole point: for regex matchers nginx takes the first that matches, so
// order is behaviour, not formatting. Brace depth is tracked so a nested block
// cannot end its parent early.
//
// The matcher is read as whitespace-separated TOKENS rather than by a regex
// stopping at the first `{`. A location regex may legitimately contain a brace —
// `[A-Za-z0-9_-]{1,64}` is a repetition quantifier, not the start of the block —
// and matching it as the opening brace silently truncates the matcher, which
// then fails to compare equal to anything and reads as "the route is missing".
// nginx itself requires the opening brace to be its own token, so the split is
// exact. For the same reason the body's brace depth is counted only from AFTER
// the real opening brace.
//
// Comments are stripped per line first, so a `{` or `}` inside prose (the file
// is heavily commented) does not derail the scan.
func edgeLocations(t *testing.T, src string) []edgeLocation {
	t.Helper()
	var found []edgeLocation
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		code, _, _ := strings.Cut(lines[i], "#")
		fields := strings.Fields(code)
		if len(fields) < 3 || fields[0] != "location" {
			continue
		}
		brace := -1
		for j, f := range fields {
			if f == "{" {
				brace = j
				break
			}
		}
		if brace < 2 {
			t.Fatalf("`%s` in %s has no opening brace token; nginx requires `{` to stand alone",
				strings.TrimSpace(code), edgeConf)
		}
		match := strings.Join(fields[1:brace], " ")

		// Slice the body off after the real opening brace, found past the end of
		// the matcher's last token so a quantifier brace cannot be mistaken for it.
		last := fields[brace-1]
		after := strings.Index(code, last) + len(last)
		open := after + strings.Index(code[after:], "{")

		rest := code[open+1:]
		depth := 1 + strings.Count(rest, "{") - strings.Count(rest, "}")
		body := []string{rest}
		for depth > 0 && i+1 < len(lines) {
			i++
			next, _, _ := strings.Cut(lines[i], "#")
			depth += strings.Count(next, "{") - strings.Count(next, "}")
			body = append(body, next)
		}
		if depth != 0 {
			t.Fatalf("unbalanced braces after `location %s` in %s", match, edgeConf)
		}
		found = append(found, edgeLocation{match: match, body: strings.Join(body, "\n")})
	}
	if len(found) == 0 {
		t.Fatalf("no location blocks parsed from %s — the scanner and the file have drifted apart", edgeConf)
	}
	return found
}

// apiLocations narrows to the blocks that decide the fate of an /api/v1 request.
func apiLocations(t *testing.T, src string) []edgeLocation {
	t.Helper()
	var found []edgeLocation
	for _, loc := range edgeLocations(t, src) {
		if strings.Contains(loc.match, "/api/v1") {
			found = append(found, loc)
		}
	}
	return found
}

// The default for /api/v1 is 404, and it has to be the LAST of the /api/v1
// blocks. nginx evaluates regex locations in file order and takes the first
// match, so an allow rule written below this one never runs — it reloads
// cleanly, `nginx -t` passes, and the route it meant to publish quietly stays
// 404. This is the guard against publishing by accident AND against failing to
// publish by accident.
func TestTheEdgeDeniesEveryAPIRouteItDoesNotExplicitlyPublish(t *testing.T) {
	api := apiLocations(t, edgeSource(t))
	if len(api) == 0 {
		t.Fatalf("%s has no /api/v1 location at all — the API would fall through to "+
			"`location /` and the ENTIRE api surface would be public", edgeConf)
	}
	last := api[len(api)-1]
	if last.match != "~ ^/api/v1/" {
		t.Fatalf("the last /api/v1 block in %s matches %q, want `~ ^/api/v1/` — "+
			"the catch-all deny must come after every allow rule, or the rules "+
			"below it are dead and their routes stay 404", edgeConf, last.match)
	}
	if !regexp.MustCompile(`return\s+404`).MatchString(last.body) {
		t.Errorf("the catch-all `location ~ ^/api/v1/` in %s does not `return 404`; "+
			"anything it does instead is applied to every unpublished API route, "+
			"including DELETE /api/v1/sessions/{id} and /calls/*", edgeConf)
	}
}

// The published set, spelled out. A route added to internal/handler/routes_api.go
// is NOT public until it is added here as well, and a route added here is public
// to the whole internet — so the set is asserted exactly rather than loosely, and
// widening it is a diff someone has to justify.
//
// What is deliberately absent matters as much as what is present:
//
//   - /api/v1/health takes no authentication at all (routes_api.go registers it
//     without the RequireAPI wrapper) and reports session counts.
//   - DELETE /api/v1/sessions/{id} cannot unlink the device on the waxum
//     backend and burns one of the handset's few linked-device slots per call.
//   - /calls/* places real outbound WhatsApp calls; WSM_CALLS_ENABLED is true in
//     production, and a ban lands on an identity that has no second copy.
func TestTheEdgePublishesOnlyTheSendAndInboundAPIRoutes(t *testing.T) {
	want := map[string]bool{
		"= /api/v1/auth/login":     true,
		"= /api/v1/auth/logout":    true,
		"= /api/v1/messages":       true,
		"= /api/v1/messages/group": true,
		"= /api/v1/messages/media": true,
		"= /api/v1/inbound":        true,
		"= /api/v1/contacts/check": true,
		// Quoted deliberately: nginx's parser treats an unquoted `{` as the start
		// of a block, so the repetition quantifier would end the location early
		// and `nginx -t` fails. Verified against nginx 1.18 on the live edge.
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}/(messages|messages/media|contacts/check)$"`: true,
	}

	got := map[string]bool{}
	for _, loc := range apiLocations(t, edgeSource(t)) {
		if loc.match == "~ ^/api/v1/" {
			continue // the catch-all deny, covered by its own test
		}
		got[loc.match] = true
	}

	for match := range want {
		if !got[match] {
			t.Errorf("%s no longer publishes `location %s` — that route is now 404 "+
				"to every public caller", edgeConf, match)
		}
	}
	for match := range got {
		if !want[match] {
			t.Errorf("%s publishes `location %s`, which is not in the reviewed allowlist. "+
				"Adding a route here exposes it to the whole internet: confirm it is safe "+
				"there, then add it to this test.", edgeConf, match)
		}
	}

	// Belt and braces: these must never appear in ANY published matcher, whatever
	// shape a future edit gives it.
	for _, forbidden := range []string{"/api/v1/health", "/api/v1/stats", "calls", "webhooks", "pairing"} {
		for match := range got {
			if strings.Contains(match, forbidden) {
				t.Errorf("`location %s` in %s publishes %q, which must stay private",
					match, edgeConf, forbidden)
			}
		}
	}
}

// nginx locations inherit proxy_set_header from server/http, but NEVER from a
// sibling location — and a location that sets even ONE header discards the whole
// inherited set. Keeping the headers in `location / {}` (where this file used to
// have them) would leave every /api/v1 block with no X-Forwarded-For at all.
//
// That failure is invisible in nginx and expensive in the app: auth.ClientIP
// then falls back to RemoteAddr, which is the WireGuard peer 10.10.0.1 for every
// public caller alike. The failed-Basic throttle is keyed on that value, so ONE
// attacker guessing secrets would throttle every API client on the internet
// simultaneously — the exact outcome this file's own X-Forwarded-For comment
// warns about.
func TestTheEdgeSetsProxyHeadersAtServerLevelSoAPILocationsInheritThem(t *testing.T) {
	src := edgeSource(t)

	for _, loc := range edgeLocations(t, src) {
		if strings.Contains(loc.body, "proxy_set_header") {
			t.Errorf("`location %s` in %s sets proxy_set_header inside the block; that "+
				"DISCARDS every header inherited from the server, X-Forwarded-For "+
				"included. Hoist them to server level so all locations share one set.",
				loc.match, edgeConf)
		}
	}

	// And they have to actually exist somewhere above the locations.
	for _, header := range []string{"Host", "X-Real-IP", "X-Forwarded-For", "X-Forwarded-Proto"} {
		if !strings.Contains(src, "proxy_set_header "+header) {
			t.Errorf("%s sets no %s header at all", edgeConf, header)
		}
	}
	if !strings.Contains(src, "$proxy_add_x_forwarded_for") {
		t.Errorf("%s does not build X-Forwarded-For with $proxy_add_x_forwarded_for; a "+
			"client-supplied value would be trusted verbatim", edgeConf)
	}
}

// The edge is the only brake on an authenticated caller. The app throttles
// FAILED HTTP Basic attempts per IP (internal/auth/middleware.go) but does not
// throttle the bearer path at all — verifying a token is one SHA-256 and an
// indexed lookup — so a leaked token is rate-limited here or nowhere. There is
// no fail2ban and no ufw on that box either.
//
// Zone names live in nginx's http context and are GLOBAL across conf.d, which is
// shared with nine other products; a collision fails the reload for all of them,
// hence the wa_ prefix on both.
func TestTheEdgeRateLimitsTheAPIWithWaPrefixedZones(t *testing.T) {
	src := edgeSource(t)

	zone := regexp.MustCompile(`limit_req_zone\s+\S+\s+zone=(\S+?):`)
	var zones []string
	for _, m := range zone.FindAllStringSubmatch(src, -1) {
		zones = append(zones, m[1])
		if !strings.HasPrefix(m[1], "wa_") {
			t.Errorf("limit_req_zone %q in %s is not wa_-prefixed; zone names are global "+
				"across the shared conf.d and a collision fails the reload for every "+
				"product on that box", m[1], edgeConf)
		}
	}
	if len(zones) < 2 {
		t.Fatalf("%s declares %d limit_req_zone(s), want at least the credential zone and "+
			"the general API zone", edgeConf, len(zones))
	}

	// A limit_req_zone written inside server{} fails `nginx -t` outright, so it
	// has to sit at file top level beside the maps.
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "limit_req_zone") && strings.HasPrefix(line, " ") {
			t.Errorf("limit_req_zone is indented in %s, which means it is inside a block; "+
				"it is an http-context directive and `nginx -t` rejects it there:\n%s",
				edgeConf, line)
		}
	}

	// The credential endpoint gets its own, tighter zone: it is a bcrypt compare
	// and the obvious brute-force target.
	var login *edgeLocation
	for _, loc := range apiLocations(t, src) {
		if loc.match == "= /api/v1/auth/login" {
			l := loc
			login = &l
		}
	}
	if login == nil {
		t.Fatalf("%s no longer publishes = /api/v1/auth/login", edgeConf)
	}
	if !strings.Contains(login.body, "limit_req zone=wa_api_auth") {
		t.Errorf("the published `= /api/v1/auth/login` in %s is not behind wa_api_auth; "+
			"the login endpoint is a bcrypt comparison exposed to the internet", edgeConf)
	}

	if !strings.Contains(src, "limit_req_status 429") {
		t.Errorf("%s does not set limit_req_status 429; nginx's default is 503, which "+
			"tells a throttled API client to retry as though the service were down", edgeConf)
	}
}
