package wsm_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The compose files are not embedded — nothing at runtime reads them — so these
// tests go to disk. `go test` runs a package with its own directory as the
// working directory, and this package is the repo root.
const (
	baseCompose     = "docker-compose.yml"
	remoteDBCompose = "docker-compose.remote-db.yml"
)

func composeSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

var serviceKey = regexp.MustCompile(`^ {2}([a-z0-9][a-z0-9._-]*):\s*$`)

// composeServices splits a compose file into service name -> that service's
// block of the file. Scanned rather than hardcoded, so a service added to the
// base file cannot quietly skip the checks below.
func composeServices(t *testing.T, src string) map[string]string {
	t.Helper()
	found, inServices, name := map[string]string{}, false, ""
	var block []string
	flush := func() {
		if name != "" {
			found[name] = strings.Join(block, "\n")
		}
		name, block = "", nil
	}
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "services:"):
			flush()
			inServices = true
		case !inServices:
			// top-level key outside services: nothing to collect
		case line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			flush()
			inServices = false
		default:
			if m := serviceKey.FindStringSubmatch(line); m != nil {
				flush()
				name = m[1]
				continue
			}
			if name != "" {
				block = append(block, line)
			}
		}
	}
	flush()
	if len(found) == 0 {
		t.Fatal("no services parsed — the scanner and the compose layout have drifted apart")
	}
	return found
}

// infraServices names the services in the BASE file that join the shared
// external network. Those, and only those, are what the overlay has to undo.
func infraServices(t *testing.T) []string {
	t.Helper()
	var names []string
	for name, block := range composeServices(t, composeSource(t, baseCompose)) {
		for _, line := range strings.Split(block, "\n") {
			if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, "networks:") &&
				strings.Contains(code, "infra") {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) < 2 {
		t.Fatalf("expected at least app and migrate to join the shared network, got %v", names)
	}
	return names
}

// The premise the overlay exists to cancel. If the base file stops declaring the
// shared stack's network as external, the overlay is dead weight and the reader
// of that diff needs to be told so here, not by puzzling over a no-op file.
func TestBaseComposeDeclaresTheSharedNetworkAsExternal(t *testing.T) {
	src := composeSource(t, baseCompose)
	if !strings.Contains(src, "external: true") || !strings.Contains(src, "master-compose-ai-net") {
		t.Fatalf("%s no longer declares master-compose-ai-net as external — "+
			"%s exists solely to undo that and should be re-examined", baseCompose, remoteDBCompose)
	}
}

// Compose MERGES a service's networks with the base file's rather than replacing
// them, so `networks: []` leaves `infra` attached and the external lookup still
// runs on a host where that network does not exist. `!reset` is the only form
// that deletes the key. A service added to the base file and forgotten here is
// exactly the regression this catches.
func TestRemoteDBOverlayResetsTheSharedNetworkOnEveryServiceThatJoinsIt(t *testing.T) {
	overlay := composeServices(t, composeSource(t, remoteDBCompose))
	for _, name := range infraServices(t) {
		t.Run(name, func(t *testing.T) {
			block, ok := overlay[name]
			if !ok {
				t.Fatalf("service %q joins the shared network in %s but %s does not override it — "+
					"`up` on a host without that network fails with "+
					"\"network declared as external, but could not be found\"",
					name, baseCompose, remoteDBCompose)
			}
			if !regexp.MustCompile(`networks:\s*!reset`).MatchString(block) {
				t.Errorf("service %q does not reset its networks in %s; compose merges this key, "+
					"so anything short of `!reset` leaves infra attached", name, remoteDBCompose)
			}
		})
	}
}

// Undeclaring the network on the services is not enough: compose resolves every
// DECLARED external network at `up`, attached or not.
func TestRemoteDBOverlayUndeclaresTheSharedNetworkItself(t *testing.T) {
	src := composeSource(t, remoteDBCompose)
	top := src[strings.Index(src, "\nnetworks:")+1:]
	if idx := strings.Index(top, "\nservices:"); idx >= 0 {
		top = top[:idx]
	}
	if !regexp.MustCompile(`infra:\s*!reset`).MatchString(top) {
		t.Errorf("%s leaves the top-level `infra` network declared; compose looks up every "+
			"declared external network at `up`, so the stack still fails on a host "+
			"that does not have it", remoteDBCompose)
	}
}

// The base file pins MySQL to the shared stack's container DNS name. On a host
// that only runs the app that name does not resolve, and the failure is a dial
// timeout at boot rather than anything that names the cause. `:?` turns it into
// a `config`-time error instead.
//
// The claim here is "every service that dials the shared MySQL is repointed in
// remote-db mode", not "every service spells WSM_MYSQL_HOST as its own env
// key" — a service can just as well consume it inside a DSN (waxum's
// DATABASE_URL does: it has no WSM_MYSQL_* env vars of its own). So the check
// is generalised to "the required-substitution form appears somewhere in the
// service's overlay block", which still catches "forgot to override" for `app`
// and `migrate` and now also covers a DSN-shaped consumer.
func TestRemoteDBOverlayRepointsMySQLForEveryServiceThatJoinsTheSharedNetwork(t *testing.T) {
	if base := composeSource(t, baseCompose); !strings.Contains(base, "WSM_MYSQL_HOST: mysqldb-master") {
		t.Fatalf("%s no longer pins WSM_MYSQL_HOST to the shared stack's container DNS — "+
			"the override in %s may now be pointless", baseCompose, remoteDBCompose)
	}
	overlay := composeServices(t, composeSource(t, remoteDBCompose))
	for _, name := range infraServices(t) {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"WSM_MYSQL_HOST", "WSM_MYSQL_PORT"} {
				want := "${" + key + ":?"
				if !strings.Contains(overlay[name], want) {
					t.Errorf("service %q in %s does not require %s...} anywhere in its overlay block — "+
						"it would dial mysqldb-master, which resolves only on the shared stack's host",
						name, remoteDBCompose, want)
				}
			}
		})
	}
}

// One .env serves host mode, container mode and this. An address written into
// the overlay would make it a single machine's file rather than a mode, and the
// next host to need it would get a stack that dials the previous host's MySQL.
func TestRemoteDBOverlayHardcodesNoAddresses(t *testing.T) {
	ip := regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	for i, line := range strings.Split(composeSource(t, remoteDBCompose), "\n") {
		code, _, _ := strings.Cut(line, "#")
		if got := ip.FindString(code); got != "" {
			t.Errorf("%s:%d hardcodes the address %s; it belongs in .env, "+
				"which is where the other two modes already read it from",
				remoteDBCompose, i+1, got)
		}
	}
}

// A mode applied to `up` and forgotten on `logs` is a trap: the stack starts
// fine and then every follow-up target fails on a network that is not missing,
// only elsewhere. Routing all of them through one $(COMPOSE) is what prevents
// that, so no recipe may spell the command out for itself.
func TestEveryMakefileRecipeInvokesComposeThroughTheSharedVariable(t *testing.T) {
	for i, line := range strings.Split(composeSource(t, "Makefile"), "\n") {
		if !strings.HasPrefix(line, "\t") || !strings.Contains(line, "docker compose") {
			continue
		}
		if !strings.Contains(line, "$(COMPOSE)") {
			t.Errorf("Makefile:%d calls `docker compose` directly: %s\n"+
				"\tuse $(COMPOSE), or REMOTE_DB=1 will apply to some targets and not others",
				i+1, strings.TrimSpace(line))
		}
	}
}

// And the variable has to actually carry the overlay, or REMOTE_DB=1 is a flag
// that changes nothing and reports success.
func TestMakefileComposeVariableAddsTheOverlayUnderRemoteDB(t *testing.T) {
	src := composeSource(t, "Makefile")
	if !regexp.MustCompile(`COMPOSE\s*:?=.*\$\(if \$\(REMOTE_DB\),[^)]*` +
		regexp.QuoteMeta(remoteDBCompose)).MatchString(src) {
		t.Errorf("the Makefile's COMPOSE variable does not add %s when REMOTE_DB is set — "+
			"`make up REMOTE_DB=1` would silently start the shared-stack configuration",
			remoteDBCompose)
	}
}

// waxum's SUPERADMIN_TOKEN is a root credential for every WhatsApp session it
// holds. The app reaches it at http://waxum:3451 over the compose network;
// nothing else may. A `ports:` block added here "just to debug" and left in
// place would put that credential's blast radius one network hop away instead
// of zero — this is the guard against forgetting to remove it.
func TestWaxumPublishesNoHostPort(t *testing.T) {
	services := composeServices(t, composeSource(t, baseCompose))
	block, ok := services["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + baseCompose)
	}
	for i, line := range strings.Split(block, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if regexp.MustCompile(`^\s*ports:\s*$`).MatchString(code) ||
			regexp.MustCompile(`^\s*-\s*["']?\d+:\d+`).MatchString(code) {
			t.Errorf("%s waxum service line %d publishes a host port: %q — "+
				"waxum is reachable ONLY at http://waxum:3451 on the compose network; "+
				"nothing else may reach it", baseCompose, i+1, strings.TrimSpace(line))
		}
	}
}

// An image on an unpinned tag is an unreviewed upgrade landing on every
// `docker compose pull`. Same reasoning as everything else this repo pins
// deliberately.
//
// The image OWNER is asserted too, and that half is not stylistic. Upstream's
// fdciabdul/waxum (and imtaqin/waxum, which it builds) cannot send
// pre-uploaded media at all: `MediaData` is a `#[serde(untagged)]` enum with
// `Url` declared first and no `deny_unknown_fields`, so a descriptor body
// matches `Url`, its six other fields are silently dropped, and waxum fetches
// the descriptor's mmg.whatsapp.net URL — the ENCRYPTED blob, which passes
// waxum's own SSRF guard — and sends that. The recipient receives an
// undecryptable file while the API answers 200 with a real message id, so
// nothing on this side notices. `get_media_data` rejects the `Uploaded`
// variant outright as well.
//
// ardihikaru/waxum's fix/uploaded-media-descriptor fixes both. Reverting this
// line to upstream is therefore a silent, data-corrupting regression on every
// media send rather than a rollback, which is exactly the class of change a
// test should refuse to let through quietly.
func TestWaxumImageIsOurForkOnAPinnedTag(t *testing.T) {
	services := composeServices(t, composeSource(t, baseCompose))
	block, ok := services["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + baseCompose)
	}
	// Match the image LINE, not the whole service block: the block's own
	// comment names the upstream images in order to explain why they must not
	// be used, and a bare strings.Contains over the block would flag that
	// explanation as the violation. Same substring-scan trap that
	// TestHandlersDoNotImportWhatsmeow parses imports to avoid.
	line := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)\s*$`).FindStringSubmatch(block)
	if line == nil {
		t.Fatalf("%s waxum service has no image line at all", baseCompose)
	}
	ref := line[1]
	if strings.HasPrefix(ref, "fdciabdul/waxum") || strings.HasPrefix(ref, "imtaqin/waxum") {
		t.Fatalf("%s waxum image = %q, an upstream build; media sends silently "+
			"deliver corrupt files on stock waxum — see this test's doc comment",
			baseCompose, ref)
	}
	m := regexp.MustCompile(`^ardihikaru/waxum:(\S+)$`).FindStringSubmatch(ref)
	if m == nil {
		t.Fatalf("%s waxum image = %q, want ardihikaru/waxum:<pinned tag>", baseCompose, ref)
	}
	tag := m[1]
	if tag == "" || tag == "latest" || strings.Contains(tag, "${WAXUM_TAG:-latest}") {
		t.Errorf("waxum image tag = %q, want a pinned version — never `latest`, "+
			"an unpinned tag is an unreviewed upgrade on every pull", tag)
	}
}

// WA_DSN (Makefile) embeds $(WSM_MYSQL_PASSWORD) inline, so any recipe using
// it MUST be @-prefixed: without that, make echoes the whole command — the
// password with it — to stdout before running it. That is how the production
// wa_dashboard password landed in a terminal scrollback on 2026-08-27, and it
// would reach a CI log or a redirected file exactly as easily. Same family as
// the `docker compose config` hazard: the secret is not printed by the thing
// you are running, it is printed by the tooling around it.
func TestNoMakefileRecipeEchoesTheDatabasePassword(t *testing.T) {
	src := composeSource(t, "Makefile")
	for i, line := range strings.Split(src, "\n") {
		// Recipe lines start with a literal tab; everything else is a
		// variable definition, a comment or a target header.
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		if !strings.Contains(line, "$(WA_DSN)") {
			continue
		}
		if !strings.HasPrefix(strings.TrimPrefix(line, "\t"), "@") {
			t.Errorf("Makefile:%d uses $(WA_DSN) without an @ prefix, so make "+
				"echoes the password: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// RATE_LIMIT_PER_SECOND is NOT a requests-per-second rate, despite the name:
// waxum's tower_governor middleware treats GovernorConfigBuilder.per_second()
// as the REPLENISH INTERVAL, in seconds, for a single token — waxum's own
// library default of 20 means one token every 20 seconds. That makes a
// BIGGER number here TIGHTER, not looser. "Raising" it toward, say, 50 (as a
// naive reading of the name suggests) would make waxum throttle 2.5x more
// aggressively than doing nothing at all, and reintroduce the pairing-poller
// 429s documented in CLAUDE.md's waxum rate-limit hazard note. The generous
// setting keeps this interval small (near 1, i.e. ~1 token/sec) and gets its
// headroom from RATE_LIMIT_BURST instead.
func TestWaxumRateLimitIntervalIsSmallBecauseItIsAnIntervalNotARate(t *testing.T) {
	services := composeServices(t, composeSource(t, baseCompose))
	block, ok := services["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + baseCompose)
	}

	perSecond := regexp.MustCompile(`RATE_LIMIT_PER_SECOND:\s*["']?(\d+)`).FindStringSubmatch(block)
	if perSecond == nil {
		t.Fatalf("%s waxum service has no RATE_LIMIT_PER_SECOND line", baseCompose)
	}
	if n, err := strconv.Atoi(perSecond[1]); err != nil || n > 2 {
		t.Errorf("waxum RATE_LIMIT_PER_SECOND = %q, want <= 2 — this is tower_governor's "+
			"per_second(), the REPLENISH INTERVAL IN SECONDS for one token, not a "+
			"requests-per-second rate. waxum's own default of 20 means one token every 20 "+
			"seconds, so a BIGGER value here throttles HARDER, not softer. Do not raise this "+
			"toward 50 to be \"generous\" — that is 2.5x tighter than doing nothing; tune "+
			"generosity through RATE_LIMIT_BURST instead", perSecond[1])
	}

	burst := regexp.MustCompile(`RATE_LIMIT_BURST:\s*["']?(\d+)`).FindStringSubmatch(block)
	if burst == nil {
		t.Fatalf("%s waxum service has no RATE_LIMIT_BURST line", baseCompose)
	}
	if n, err := strconv.Atoi(burst[1]); err != nil || n < 100 {
		t.Errorf("waxum RATE_LIMIT_BURST = %q, want >= 100 — waxum is reachable only on the "+
			"compose network and only WSM talks to it, so there is no reason to keep its "+
			"public-internet-facing default burst of ~3", burst[1])
	}
}
