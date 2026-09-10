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
//   - GET /api/v1/sessions and GET /api/v1/sessions/{id} ARE published (below);
//     GET /api/v1/health is too, and unauthenticated by design (routes_api.go
//     registers it without the RequireAPI wrapper) — it reports session counts to
//     anyone who asks. See TestTheEdgeGatesThePublishedSessionReadRoutesToSafeMethodsOnly
//     for why the two session paths do not also publish every other method.
//   - DELETE /api/v1/sessions/{id} shares ITS PATH with the now-published detail
//     view above, so it reads as published in a bare path scan. It is not: the
//     path is published GET/HEAD only, and DELETE is denied at the edge by the
//     $request_method gate on that location — it cannot unlink the device on the
//     waxum backend and burns one of the handset's few linked-device slots per
//     call.
//   - /calls/* places real outbound WhatsApp calls; WSM_CALLS_ENABLED is true in
//     production, and a ban lands on an identity that has no second copy.
func TestTheEdgePublishesOnlyTheSendAndInboundAPIRoutes(t *testing.T) {
	want := map[string]bool{
		"= /api/v1/auth/login":     true,
		"= /api/v1/auth/logout":    true,
		"= /api/v1/messages":       true,
		"= /api/v1/messages/group": true,
		// Since the multi-host failover work, /messages/media keeps its own exact
		// location, separate from the bundled `/messages`/`/messages/group`
		// alternation, so it can opt OUT of the server-level
		// proxy_request_buffering on that failover needs — buffering a 110 MB
		// upload to disk before forwarding a byte roughly doubles a large send.
		// See TestMediaLocationsAreTheOnlyExceptionToRequestBufferingBeingOn.
		"= /api/v1/messages/media": true,
		"= /api/v1/inbound":        true,
		"= /api/v1/contacts/check": true,
		// Quoted deliberately: nginx's parser treats an unquoted `{` as the start
		// of a block, so the repetition quantifier would end the location early
		// and `nginx -t` fails. Verified against nginx 1.18 on the live edge.
		//
		// Split into two regexes, both anchored with `$` so they cannot overlap,
		// for the same buffering reason as the exact /messages/media location
		// above: the session-scoped media route needs its own
		// proxy_request_buffering off, which a shared alternation could not give
		// to only one branch.
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}/(messages|contacts/check)$"`: true,
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}/messages/media$"`:            true,
		// Read-only session views, published 2026-09-10. Both are method-gated to
		// GET/HEAD only (TestTheEdgeGatesThePublishedSessionReadRoutesToSafeMethodsOnly);
		// the bare presence of the path here does not mean POST/DELETE are public too.
		"= /api/v1/sessions":                         true,
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}$"`: true,
		// Unauthenticated by design — see the doc comment above.
		"= /api/v1/health": true,
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
	// shape a future edit gives it. "/api/v1/health" is deliberately NOT in this
	// list any more: it was published 2026-09-10 for uptime probing from outside
	// the WireGuard tunnel (see the doc comment above), so its presence here would
	// make this test fail against the config it is meant to be checking.
	for _, forbidden := range []string{"/api/v1/stats", "calls", "webhooks", "pairing"} {
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

	// /api/v1/health is unauthenticated, so it must NOT share wa_api's budget with
	// authenticated message sends — an anonymous scraper could otherwise spend the
	// same allowance. Mirrors how = /api/v1/auth/login above is pinned to
	// wa_api_auth rather than the general zone.
	var health *edgeLocation
	for _, loc := range apiLocations(t, src) {
		if loc.match == "= /api/v1/health" {
			l := loc
			health = &l
		}
	}
	if health == nil {
		t.Fatalf("%s no longer publishes = /api/v1/health", edgeConf)
	}
	if !strings.Contains(health.body, "limit_req zone=wa_health") {
		t.Errorf("the published `= /api/v1/health` in %s is not behind its own wa_health "+
			"zone; an anonymous caller could then spend the same budget authenticated "+
			"message sends draw on", edgeConf)
	}
	if strings.Contains(health.body, "limit_req zone=wa_api ") {
		t.Errorf("the published `= /api/v1/health` in %s is rate-limited by wa_api, not "+
			"its own wa_health zone; an anonymous caller could spend the same budget "+
			"authenticated message sends draw on", edgeConf)
	}

	if !strings.Contains(src, "limit_req_status 429") {
		t.Errorf("%s does not set limit_req_status 429; nginx's default is 503, which "+
			"tells a throttled API client to retry as though the service were down", edgeConf)
	}
}

// GET /api/v1/sessions, GET /api/v1/sessions/{id} and GET /api/v1/health are
// published as of 2026-09-10 — but nginx matches a location by PATH, not
// method, so publishing those paths bare would ALSO publish POST
// /api/v1/sessions (creates a session) and DELETE /api/v1/sessions/{id}
// (cannot unlink the device on the waxum backend and burns one of the
// handset's few linked-device slots per call — see CLAUDE.md). Each of the
// three locations must therefore answer everything except GET/HEAD with a
// bare 404 before it ever reaches proxy_pass, or a future edit that drops the
// gate republishes session creation and deletion to the whole internet with no
// signal beyond a clean `nginx -t`.
//
// The condition regex asserted here must never contain a brace: edgeLocations
// (see its doc comment) finds a location body's end by counting `{`/`}` per
// line, so a guard written with a quantifier (e.g. `{1,2}`) would silently
// break that scan. `^(GET|HEAD)$` has none.
//
// The matcher below scans up to the FIRST `{` rather than the first `)`: the
// guarded condition is `$request_method !~ ^(GET|HEAD)$`, which itself
// contains a `)` (closing the `(GET|HEAD)` group) before the `)` that closes
// the `if`. A naive `[^)]*` stops at that inner paren and never matches at
// all — caught by running this test against the real generated block before
// trusting it.
func TestTheEdgeGatesThePublishedSessionReadRoutesToSafeMethodsOnly(t *testing.T) {
	src := edgeSource(t)

	gate := regexp.MustCompile(`if\s*\(\s*\$request_method\s*!~[^{]*\)\s*\{\s*return\s+404`)

	wantGated := map[string]string{
		"= /api/v1/sessions":                         "wa_api ",
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}$"`: "wa_api ",
		"= /api/v1/health":                           "wa_health",
	}

	found := map[string]bool{}
	for _, loc := range apiLocations(t, src) {
		zone, ok := wantGated[loc.match]
		if !ok {
			continue
		}
		found[loc.match] = true
		if !gate.MatchString(loc.body) {
			t.Errorf("`location %s` in %s has no $request_method guard returning 404; "+
				"without it, publishing this READ path also publishes DELETE "+
				"/api/v1/sessions/{id} (cannot unlink the device on the waxum backend, "+
				"burns one of the handset's few linked-device slots per call) or POST "+
				"/api/v1/sessions (creates a session) to every caller on the internet",
				loc.match, edgeConf)
		}
		if !strings.Contains(loc.body, "limit_req zone="+zone) {
			t.Errorf("`location %s` in %s is not rate-limited by zone=%s as expected",
				loc.match, edgeConf, zone)
		}
	}
	for match := range wantGated {
		if !found[match] {
			t.Errorf("%s no longer publishes `location %s` — expected it gated to "+
				"GET/HEAD, not absent", edgeConf, match)
		}
	}
}

// ── MULTI-HOST FAILOVER (wa.conf) AND THE SEPARATE waxum.conf VHOST ────────────────
//
// docs/deploy-nginx-waxum.conf.example is waxum's own admin console vhost, deployed
// alongside wa.conf on the same box as /etc/nginx/conf.d/waxum.conf. It is a
// SEPARATE file on purpose (see its own top-of-file comment): waxum's console is a
// normal app rooted at `/` with its own `/api/v1` tree and `/swagger-ui`, so it must
// not inherit wa.conf's API allowlist or its catch-all 404 deny.
const waxumConf = "docs/deploy-nginx-waxum.conf.example"

func waxumSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(waxumConf)
	if err != nil {
		t.Fatalf("read %s: %v", waxumConf, err)
	}
	return string(b)
}

// namedBlock extracts the body of the first `<keyword> <name> { … }` block in src —
// used here for `upstream wa_pool { … }` and similar. Brace-balanced, comments
// stripped per line first, same discipline as edgeLocations.
func namedBlock(t *testing.T, src, keyword, name string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	prefix := keyword + " " + name
	for i := 0; i < len(lines); i++ {
		code, _, _ := strings.Cut(lines[i], "#")
		trimmed := strings.TrimSpace(code)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		open := strings.Index(code, "{")
		if open < 0 {
			t.Fatalf("`%s` has no opening brace on its own line in the source", prefix)
		}
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
			t.Fatalf("unbalanced braces inside `%s { … }`", prefix)
		}
		return strings.Join(body, "\n")
	}
	t.Fatalf("no `%s { … }` block found", prefix)
	return ""
}

// A `backup` server with no max_fails/fail_timeout of its own still parses cleanly —
// `nginx -t` accepts it — but silently falls back to nginx's own defaults
// (max_fails=1 fail_timeout=10s), and the whole point of stating both explicitly on
// every peer is that a future edit to the primary's line cannot leave the backup
// drifted without it showing in a diff. This also catches the primary itself being
// mismarked `backup` (which would mean wa_pool never sends live traffic to the
// mini-PC at all) and a backup silently promoted to primary (which would defeat the
// "only after every non-backup peer is down" ordering the whole failover story rests
// on).
func TestWaPoolMarksOnlyTheLaptopAsBackupAndBothPeersCarryExplicitHealthParameters(t *testing.T) {
	body := namedBlock(t, edgeSource(t), "upstream", "wa_pool")

	primary := regexp.MustCompile(`server\s+10\.10\.0\.4:18801\s+max_fails=\d+\s+fail_timeout=\S+;`)
	if !primary.MatchString(body) {
		t.Errorf("wa_pool in %s has no `server 10.10.0.4:18801 max_fails=… fail_timeout=…;` "+
			"line with no `backup` keyword — the mini-PC must be the primary, taking live "+
			"traffic under normal conditions", edgeConf)
	}
	if regexp.MustCompile(`server\s+10\.10\.0\.4:18801[^;]*\bbackup\b`).MatchString(body) {
		t.Errorf("wa_pool in %s marks the mini-PC (10.10.0.4) `backup`; that means the "+
			"pool never sends live traffic there under normal conditions and defeats the "+
			"primary/backup ordering the whole design rests on", edgeConf)
	}

	backup := regexp.MustCompile(`server\s+10\.10\.0\.3:18801\s+backup\s+max_fails=\d+\s+fail_timeout=\S+;`)
	if !backup.MatchString(body) {
		t.Errorf("wa_pool in %s has no `server 10.10.0.3:18801 backup max_fails=… "+
			"fail_timeout=…;` line; without explicit health parameters on the backup peer "+
			"too, a future edit to the primary's line can drift the backup onto nginx's own "+
			"defaults without it showing in a diff", edgeConf)
	}
}

// declaredUpstreamNames collects every `upstream <name> {` group header in src.
func declaredUpstreamNames(src string) map[string]bool {
	names := map[string]bool{}
	up := regexp.MustCompile(`(?m)^\s*upstream\s+(\S+)\s*\{`)
	for _, m := range up.FindAllStringSubmatch(src, -1) {
		names[m[1]] = true
	}
	return names
}

// mapValues collects the second-column value of every entry line inside a named
// `map $host $var { … }` block, skipping the `hostnames;` directive and any bare
// `default …;` line's own value only when it is the empty string (nginx accepts an
// unmatched host resolving to "" as the deliberate 444 guard, not a typo'd group).
func mapValues(t *testing.T, src, varName string) map[string]string {
	t.Helper()
	body := namedBlock(t, src, "map", "$host $"+varName+" {")
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		code, _, _ := strings.Cut(line, "#")
		fields := strings.Fields(code)
		if len(fields) != 2 {
			continue
		}
		host := strings.TrimSuffix(fields[0], ";")
		val := strings.Trim(strings.TrimSuffix(fields[1], ";"), `"`)
		if host == "hostnames" {
			continue
		}
		out[host] = val
	}
	return out
}

// A map entry that names an upstream group nginx has never declared does not fail
// `nginx -t` — proxy_pass with a variable falls back to treating the value as a
// hostname and only fails LOUDLY, in error.log, at request time against whatever
// resolver is (or is not) configured. A typo'd group name here is exactly the kind
// of mistake that reloads cleanly and only shows up when someone requests that host.
func TestEveryHostMapEntryInEitherEdgeFileResolvesToADeclaredUpstreamGroup(t *testing.T) {
	cases := []struct {
		file    string
		src     string
		varName string
	}{
		{edgeConf, edgeSource(t), "wa_backend"},
		{waxumConf, waxumSource(t), "waxum_backend"},
	}
	for _, c := range cases {
		declared := declaredUpstreamNames(c.src)
		for host, group := range mapValues(t, c.src, c.varName) {
			if group == "" {
				continue // the deliberate 444-guard default
			}
			if !declared[group] {
				t.Errorf("%s maps host %q to upstream group %q, which is not declared by any "+
					"`upstream %s { … }` block in this file; proxy_pass would fall back to "+
					"resolving %q as a hostname at REQUEST time and fail only in error.log",
					c.file, host, group, group, group)
			}
		}
	}
}

// Deliberately no 4xx: a 4xx from this app is an answer ABOUT the request (most
// concretely, a 409 meaning the primary's WhatsApp session is disconnected — see
// CLAUDE.md's "Connected means three different things" and orphaned-client-wedge
// notes), not evidence the backend process is down. Retrying it against wa_pool's
// backup would silently resend from a different paired phone number instead of
// surfacing the real state to the caller. proxy_next_upstream_tries caps this at one
// retry (attempt + 1 more), not nginx's unbounded default.
func TestProxyNextUpstreamNamesOnlyErrorTimeoutAndFiveXXNever4xxAndCapsRetriesAtTwo(t *testing.T) {
	src := edgeSource(t)

	next := regexp.MustCompile(`(?m)^\s*proxy_next_upstream\s+([^;]+);`)
	m := next.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s declares no proxy_next_upstream at all; failover to wa_pool's backup "+
			"would then use nginx's bare default (error timeout), which is undocumented "+
			"here and not exercised by a real test", edgeConf)
	}
	tokens := strings.Fields(m[1])
	want := map[string]bool{"error": true, "timeout": true,
		"http_500": true, "http_502": true, "http_503": true, "http_504": true}
	got := map[string]bool{}
	for _, tok := range tokens {
		got[tok] = true
		if strings.HasPrefix(tok, "http_4") {
			t.Errorf("proxy_next_upstream in %s includes %q; a 4xx is the app answering the "+
				"request, not evidence wa_pool's primary is down, and retrying it on the "+
				"backup sends the next attempt from a DIFFERENT paired phone number", edgeConf, tok)
		}
	}
	for tok := range want {
		if !got[tok] {
			t.Errorf("proxy_next_upstream in %s is missing %q", edgeConf, tok)
		}
	}

	tries := regexp.MustCompile(`(?m)^\s*proxy_next_upstream_tries\s+(\d+);`)
	tm := tries.FindStringSubmatch(src)
	if tm == nil {
		t.Fatalf("%s sets no proxy_next_upstream_tries; nginx's own default is unbounded, "+
			"which would let one request round-trip the whole pool before giving up", edgeConf)
	}
	if tm[1] != "2" {
		t.Errorf("proxy_next_upstream_tries in %s is %s, want 2 (the original attempt plus "+
			"exactly one retry)", edgeConf, tm[1])
	}
}

// Failover cannot replay a body nginx never buffered, so proxy_request_buffering must
// be `on` at server level for wa_pool's backup to ever receive a POST. But buffering
// a 110 MB media upload to disk before forwarding a byte roughly doubles a large
// send's wall-clock cost on a box shared with nine other products, and media draws no
// POST failover benefit anyway (nginx does not retry a non-idempotent method already
// sent to a peer without `non_idempotent`, which this file deliberately omits — see
// the comment above proxy_next_upstream). So exactly the two media locations opt back
// out, and nowhere else should.
func TestRequestBufferingIsOnAtServerLevelAndOffOnlyInTheTwoMediaLocations(t *testing.T) {
	src := edgeSource(t)

	serverLevel := regexp.MustCompile(`(?m)^\s{4}proxy_request_buffering\s+on;\s*$`)
	if !serverLevel.MatchString(src) {
		t.Fatalf("%s has no server-level `proxy_request_buffering on;`; with it off, no POST "+
			"— including a real message send — could ever fail over to wa_pool's backup, "+
			"because nginx would have nothing buffered left to replay", edgeConf)
	}

	var offLocations []string
	for _, loc := range apiLocations(t, src) {
		if strings.Contains(loc.body, "proxy_request_buffering off") {
			offLocations = append(offLocations, loc.match)
		}
	}
	wantOff := map[string]bool{
		"= /api/v1/messages/media":                                  true,
		`~ "^/api/v1/sessions/[A-Za-z0-9_-]{1,64}/messages/media$"`: true,
	}
	for _, match := range offLocations {
		if !wantOff[match] {
			t.Errorf("`location %s` in %s sets proxy_request_buffering off; only the two "+
				"media locations should opt out of the failover buffering enabled at server "+
				"level, everything else needs it ON to fail over a POST at all",
				match, edgeConf)
		}
	}
	for match := range wantOff {
		found := false
		for _, m := range offLocations {
			if m == match {
				found = true
			}
		}
		if !found {
			t.Errorf("`location %s` in %s does not set proxy_request_buffering off; a 110 MB "+
				"media upload would be buffered to disk before forwarding a byte, roughly "+
				"doubling the cost of a large send for no failover benefit (media draws "+
				"none — see the proxy_next_upstream comment)", match, edgeConf)
		}
	}
}

// waxum's console has no username/password of its own (see waxum.conf's top-of-file
// comment); the source allowlist is the first of two gates in front of a console
// whose own login is a bare root token, so it must fail closed. `deny all` missing,
// or not last, would leave some untested slice of the internet able to reach
// HTTP Basic directly (still a real gate, but not the defense-in-depth this vhost is
// built around).
func TestTheWaxumSourceAllowlistEndsInDenyAll(t *testing.T) {
	src := waxumSource(t)

	lines := strings.Split(src, "\n")
	var accessLines []string
	for _, l := range lines {
		code, _, _ := strings.Cut(l, "#")
		trimmed := strings.TrimSpace(code)
		if strings.HasPrefix(trimmed, "allow ") || strings.HasPrefix(trimmed, "deny ") {
			accessLines = append(accessLines, trimmed)
		}
	}
	if len(accessLines) == 0 {
		t.Fatalf("%s has no allow/deny lines at all; waxum's console has no login of its "+
			"own besides HTTP Basic, and this file's whole design assumes a source-IP "+
			"allowlist stands in front of that", waxumConf)
	}
	last := accessLines[len(accessLines)-1]
	if last != "deny all;" {
		t.Errorf("the last allow/deny line in %s is %q, want `deny all;` — without a "+
			"trailing deny-all, any address not explicitly matched by an earlier `allow` "+
			"falls through to nginx's own default of ALLOW", waxumConf, last)
	}
	for _, want := range []string{
		"allow 10.10.0.0/24;",
		"allow 103.161.62.0/24;",
		"allow 157.85.213.59/32;",
		"allow 202.152.143.147/32;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%s is missing `%s`", waxumConf, want)
		}
	}
}

// HTTP Basic is what actually stands behind the source allowlist above (its weakest
// line, 103.161.62.0/24, admits every other customer of that ISP too) — so it must
// point at the real, shared WSM admin credential file, not a stray path that would
// leave the console reachable by IP alone.
func TestTheWaxumFileGatesWithHttpBasicAgainstTheSharedHtpasswdFile(t *testing.T) {
	src := waxumSource(t)
	if !strings.Contains(src, `auth_basic_user_file /etc/nginx/wa_waxum.htpasswd;`) {
		t.Errorf("%s does not set `auth_basic_user_file /etc/nginx/wa_waxum.htpasswd;` — "+
			"without it the console is protected only by the source allowlist, whose "+
			"weakest entry (103.161.62.0/24) admits an entire ISP's customer base",
			waxumConf)
	}
	if !strings.Contains(src, `auth_basic "waxum console";`) && !regexp.MustCompile(`auth_basic\s+"[^"]+";`).MatchString(src) {
		t.Errorf("%s sets no auth_basic realm; without one HTTP Basic is not actually "+
			"enabled on this vhost", waxumConf)
	}
}

// The whole point of this vhost: the operator authenticates once via HTTP Basic
// above, and nginx injects waxum's own root-token cookie so its token-paste login
// page never appears. Losing this line would not fail loudly — the console would
// just fall back to showing that login page, which reads as "waxum is misconfigured"
// rather than "the edge stopped injecting the cookie".
func TestTheWaxumFileInjectsTheConsoleCookieAtServerLevel(t *testing.T) {
	src := waxumSource(t)
	cookieHeader := regexp.MustCompile(`proxy_set_header\s+Cookie\s+\$wa_waxum_console_cookie;`)
	if !cookieHeader.MatchString(src) {
		t.Errorf("%s does not `proxy_set_header Cookie $wa_waxum_console_cookie;` — without "+
			"it, waxum's own bare-token login page would appear to the operator instead of "+
			"being skipped", waxumConf)
	}
	if !strings.Contains(src, "include /etc/nginx/wa_waxum_tokens.conf;") {
		t.Errorf("%s does not `include /etc/nginx/wa_waxum_tokens.conf;` — that file is "+
			"where $wa_waxum_console_cookie is defined; without the include the variable "+
			"does not exist and `nginx -t` would fail", waxumConf)
	}
}

// nginx locations inherit proxy_set_header from server/http but never from a sibling
// location, and a location that sets even ONE header discards the WHOLE inherited
// set — for waxum.conf that would silently drop the Cookie injection the entire file
// exists to provide, on just that one location, the next time someone adds a
// location without noticing this rule.
func TestNoLocationInEitherEdgeFileSetsAProxyHeader(t *testing.T) {
	for _, c := range []struct {
		file string
		src  string
	}{
		{edgeConf, edgeSource(t)},
		{waxumConf, waxumSource(t)},
	} {
		for _, loc := range edgeLocations(t, c.src) {
			if strings.Contains(loc.body, "proxy_set_header") {
				t.Errorf("`location %s` in %s sets proxy_set_header inside the block; that "+
					"discards every header inherited from the server — in %s that includes "+
					"the Cookie injection the whole vhost exists to provide",
					loc.match, c.file, c.file)
			}
		}
	}
}

// Neither committed file may ever contain a real secret: the WSM admin password, its
// bcrypt hash, or SUPERADMIN_TOKEN. Both live outside conf.d (_data/paswd,
// wa_waxum_tokens.conf) specifically so these files can be safely mirrored into git.
// This asserts on SHAPE — a bcrypt hash's own `$2a$`/`$2b$`/`$2y$` prefix, and a
// `waxum_console=` cookie value that is anything other than the nginx variable that
// stands in for it — rather than any specific known secret, so it also catches a
// FUTURE token pasted in during debugging and forgotten.
func TestNeitherEdgeFileContainsALiteralTokenPasswordOrHash(t *testing.T) {
	bcrypt := regexp.MustCompile(`\$2[aby]\$`)
	// A real cookie value looks like `waxum_console=<40+ opaque characters>`; the
	// only value this template may ever write is the nginx variable itself, or the
	// literal placeholder `<SUPERADMIN_TOKEN>` used in prose to describe it.
	literalCookie := regexp.MustCompile(`waxum_console=(?:[^$\n<][^\n]{5,})`)

	for _, c := range []struct {
		file string
		src  string
	}{
		{edgeConf, edgeSource(t)},
		{waxumConf, waxumSource(t)},
	} {
		if bcrypt.MatchString(c.src) {
			t.Errorf("%s contains what looks like a bcrypt hash ($2a$/$2b$/$2y$ prefix); "+
				"this file is committed to git and mirrored verbatim to the live edge",
				c.file)
		}
		if m := literalCookie.FindString(c.src); m != "" {
			t.Errorf("%s contains a literal `waxum_console=…` value (%q) instead of the "+
				"$wa_waxum_console_cookie variable; that would commit SUPERADMIN_TOKEN, a "+
				"root credential for every session on the instance, straight into git",
				c.file, m)
		}
	}
}

// mapNames, zoneNames and upstreamNames scan for every declared name of that kind in
// src, http-context directives whose names are GLOBAL across everything conf.d loads.
func mapNames(src string) []string {
	re := regexp.MustCompile(`(?m)^\s*map\s+\$\S+\s+\$(\S+)\s*\{`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func zoneNames(src string) []string {
	re := regexp.MustCompile(`zone=([A-Za-z0-9_]+):`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func upstreamNames(src string) []string {
	var out []string
	for name := range declaredUpstreamNames(src) {
		out = append(out, name)
	}
	return out
}

// map/limit_req_zone/upstream names live in nginx's http context and are GLOBAL
// across every file conf.d loads — a name defined in both wa.conf and waxum.conf
// fails `nginx -t` for the WHOLE shared box, not just one product's vhost. Every
// name must also carry the prefix for the file that owns it, so a collision with one
// of the nine OTHER products sharing this edge is visible in a review as a plain
// naming mismatch rather than a live reload failure.
func TestNoMapOrZoneOrUpstreamNameIsDefinedTwiceAcrossTheTwoEdgeFilesAndAllAreProductPrefixed(t *testing.T) {
	wa := edgeSource(t)
	waxum := waxumSource(t)

	type named struct {
		kind, name, file string
	}
	var all []named
	for _, n := range mapNames(wa) {
		all = append(all, named{"map", n, edgeConf})
	}
	for _, n := range zoneNames(wa) {
		all = append(all, named{"limit_req_zone", n, edgeConf})
	}
	for _, n := range upstreamNames(wa) {
		all = append(all, named{"upstream", n, edgeConf})
	}
	for _, n := range mapNames(waxum) {
		all = append(all, named{"map", n, waxumConf})
	}
	for _, n := range zoneNames(waxum) {
		all = append(all, named{"limit_req_zone", n, waxumConf})
	}
	for _, n := range upstreamNames(waxum) {
		all = append(all, named{"upstream", n, waxumConf})
	}

	seen := map[string][]string{}
	for _, e := range all {
		key := e.kind + " " + e.name
		seen[key] = append(seen[key], e.file)
	}
	for key, files := range seen {
		if len(files) > 1 {
			t.Errorf("%s is declared in more than one file (%s); http-context names are "+
				"global across conf.d and a duplicate fails `nginx -t` for the whole "+
				"shared box, not just these two files", key, strings.Join(files, ", "))
		}
	}

	for _, e := range all {
		wantPrefix := "wa_"
		if e.file == waxumConf {
			wantPrefix = "waxum_"
		}
		// wa_connection_upgrade and waxum_connection_upgrade are map names too and
		// already carry the right prefix; nothing here needs excluding.
		if !strings.HasPrefix(e.name, wantPrefix) {
			t.Errorf("%s %q in %s is not %q-prefixed; a name that reads as generic can "+
				"collide with one of the nine other products sharing this edge and fail "+
				"the reload for all of them", e.kind, e.name, e.file, wantPrefix)
		}
	}
}
