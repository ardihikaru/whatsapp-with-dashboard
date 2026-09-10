package wsm_test

import (
	"regexp"
	"strings"
	"testing"
)

// waxum's NATS integration is always compiled in and gated purely by whether
// NATS_URL is set (NatsConfig::from_env() returns None otherwise, and every
// publish becomes a no-op). The base file sets it unconditionally, pointed at
// the shared stack's container DNS name — the same pattern DATABASE_URL
// already uses two lines above it.
func TestTheBaseComposeGivesWaxumItsNatsURLByContainerDNS(t *testing.T) {
	services := composeServices(t, composeSource(t, baseCompose))
	block, ok := services["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + baseCompose)
	}
	if !regexp.MustCompile(`(?m)NATS_URL:\s*nats://nats-master:4222\s*$`).MatchString(block) {
		t.Errorf("%s waxum service has no `NATS_URL: nats://nats-master:4222` line — "+
			"without it waxum's NatsConfig::from_env() returns None and every NATS publish "+
			"silently becomes a no-op, exactly as if the integration were never wired up",
			baseCompose)
	}
}

// waxum's async-nats 0.38 client silently IGNORES credentials embedded in a
// NATS URL: ServerAddr parses username()/password()/has_user_pass(), but
// has_user_pass() is called nowhere in connector.rs or options.rs —
// connect_info.user/.pass are populated exclusively from options.auth, which
// only user_and_password()/token()/an auth callback ever set. So
// `nats://user:pass@host:4222` connects UNAUTHENTICATED, with no error and no
// log line anywhere — a URL that LOOKS like it carries a credential and
// doesn't. That is exactly the mistake a future reader makes by
// pattern-matching on DATABASE_URL, which genuinely does carry its
// credentials inline. This test scans BOTH compose files and refuses any
// NATS_URL containing "@".
func TestTheNatsURLNeverCarriesEmbeddedCredentials(t *testing.T) {
	for _, file := range []string{baseCompose, remoteDBCompose} {
		for i, line := range strings.Split(composeSource(t, file), "\n") {
			code, _, _ := strings.Cut(line, "#")
			if !strings.Contains(code, "NATS_URL:") {
				continue
			}
			if strings.Contains(code, "@") {
				t.Errorf("%s:%d embeds a credential in NATS_URL: %s\n"+
					"waxum's async-nats 0.38 client silently IGNORES userinfo in a NATS URL — "+
					"has_user_pass() is never called, so this connects UNAUTHENTICATED with no "+
					"error and no log line. Credentials belong in NATS_TOKEN, never in this URL",
					file, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The base file pins waxum's NATS address to the shared stack's container DNS
// name, same as it does for MySQL. On a host that runs only this stack that
// name does not resolve (remote-db mode drops master-compose-ai-net
// entirely), so the overlay must repoint it exactly as it already repoints
// DATABASE_URL for MySQL — the precedent this test mirrors is
// TestRemoteDBOverlayRepointsMySQLForEveryServiceThatJoinsTheSharedNetwork.
func TestTheRemoteDBOverlayRepointsNatsForWaxumJustAsItRepointsMySQL(t *testing.T) {
	if base := composeSource(t, baseCompose); !strings.Contains(base, "NATS_URL: nats://nats-master:4222") {
		t.Fatalf("%s no longer pins waxum's NATS_URL to the shared stack's container DNS — "+
			"the override in %s may now be pointless", baseCompose, remoteDBCompose)
	}
	services := composeServices(t, composeSource(t, remoteDBCompose))
	block, ok := services["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + remoteDBCompose)
	}
	for _, key := range []string{"NATS_HOST", "NATS_PORT"} {
		want := "${" + key + ":?"
		if !strings.Contains(block, want) {
			t.Errorf("waxum's overlay block in %s does not require %s...} anywhere — "+
				"it would dial nats-master, which resolves only on the shared stack's host, "+
				"the same failure mode TestRemoteDBOverlayRepointsMySQLForEveryServiceThatJoinsTheSharedNetwork "+
				"guards against for MySQL", remoteDBCompose, want)
		}
	}
}

// NATS_TOKEN is a send credential: waxum runs a durable pull consumer on
// wa.send.> and executes outbound sends delivered there, bypassing WSM's
// bearer tokens, the nginx edge allowlist and its per-client throttles
// entirely. Every mention of it in either compose file must be a
// substitution, never a literal value — modelled on
// TestNoMakefileRecipeEchoesTheDatabasePassword, same reasoning: the secret
// belongs in .env, and a literal here would land it in git.
func TestNoComposeFileEchoesTheNatsToken(t *testing.T) {
	// Matches the compose environment KEY `NATS_TOKEN:` and captures whatever
	// follows it as the VALUE — the key itself is compose's own map key and
	// legitimately spells the variable's name; only the value is scrutinised.
	keyLine := regexp.MustCompile(`^\s*NATS_TOKEN:\s*(.*)$`)
	validValue := regexp.MustCompile(`^\$\{NATS_TOKEN(:-[^}]*)?(:\?[^}]*)?\}$`)
	for _, file := range []string{baseCompose, remoteDBCompose} {
		for i, line := range strings.Split(composeSource(t, file), "\n") {
			code, _, _ := strings.Cut(line, "#")
			m := keyLine.FindStringSubmatch(code)
			if m == nil {
				continue
			}
			value := strings.TrimSpace(m[1])
			if value == "" {
				// `NATS_TOKEN:` with nothing after it (e.g. the shorthand
				// that pulls the name from env_file) carries no literal.
				continue
			}
			if !validValue.MatchString(value) {
				t.Errorf("%s:%d sets NATS_TOKEN to a literal value instead of a "+
					"${NATS_TOKEN...} substitution: %s\n"+
					"NATS_TOKEN authorises outbound WhatsApp sends over waxum's wa.send.> "+
					"consumer, bypassing WSM's bearer tokens, the nginx edge allowlist and its "+
					"per-client throttles entirely — a literal value here would commit that "+
					"credential straight into git", file, i+1, strings.TrimSpace(line))
			}
		}
	}
}
