package wsm_test

import (
	"os"
	"regexp"
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
func TestRemoteDBOverlayRepointsMySQLForEveryServiceThatJoinsTheSharedNetwork(t *testing.T) {
	if base := composeSource(t, baseCompose); !strings.Contains(base, "WSM_MYSQL_HOST: mysqldb-master") {
		t.Fatalf("%s no longer pins WSM_MYSQL_HOST to the shared stack's container DNS — "+
			"the override in %s may now be pointless", baseCompose, remoteDBCompose)
	}
	overlay := composeServices(t, composeSource(t, remoteDBCompose))
	for _, name := range infraServices(t) {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"WSM_MYSQL_HOST", "WSM_MYSQL_PORT"} {
				want := key + ": ${" + key + ":?"
				if !strings.Contains(overlay[name], want) {
					t.Errorf("service %q in %s does not override %s as %s...} — "+
						"it would dial mysqldb-master, which resolves only on the shared stack's host",
						name, remoteDBCompose, key, want)
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
