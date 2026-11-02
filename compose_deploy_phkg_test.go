package wsm_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
)

// deploy/phkg/ is the bundle shipped to a host that has NO SOURCE CHECKOUT:
// the images arrive as tar.gz saved from the production host and are loaded
// with `docker load`, and the compose file, the .env template and the runbook
// are the only text that travels with them.
//
// That is what makes these tests worth having. Every other compose file in
// this repo is exercised the moment someone runs `make up` next to the source
// it builds from; this one is only ever run on a machine where nothing can be
// rebuilt and no test suite exists. A mistake here surfaces as a failed deploy
// on a remote host, hours later, with no repo to read.
const (
	phkgCompose = "deploy/phkg/docker-compose.yml"
	phkgEnv     = "deploy/phkg/.env.template"
)

// A `build:` key is the single most likely way this file rots: it is present
// on two services in the base file, and flattening the base + remote-db pair
// by hand is exactly the operation that carries it along by accident. The
// target host has no build context, so compose fails at `up` with a path
// error that reads like a missing file rather than the wrong compose file.
func TestPHKGDeployComposeBuildsNothing(t *testing.T) {
	for name, block := range composeServices(t, composeSource(t, phkgCompose)) {
		for i, line := range strings.Split(block, "\n") {
			code, _, _ := strings.Cut(line, "#")
			if regexp.MustCompile(`^\s*build:\s*$`).MatchString(code) {
				t.Errorf("%s service %q line %d declares build: — the deploy host has "+
					"no source checkout; every service must run from a loaded image",
					phkgCompose, name, i+1)
			}
		}
	}
}

// Every service must therefore name an image, and name it on a tag that says
// WHICH build it is. `:local` and `:latest` are the tags the build machine
// used; on a host with no repo to be local to, and no registry to pull a
// newer `latest` from, they identify nothing. load-images.sh applies dated
// aliases after `docker load` precisely so this file can name them.
func TestPHKGDeployComposePinsEveryImageToATraceableTag(t *testing.T) {
	imageLine := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)\s*$`)
	for name, block := range composeServices(t, composeSource(t, phkgCompose)) {
		m := imageLine.FindStringSubmatch(block)
		if m == nil {
			t.Errorf("%s service %q has no image line", phkgCompose, name)
			continue
		}
		ref := m[1]
		_, tag, ok := strings.Cut(ref, ":")
		if !ok {
			t.Errorf("%s service %q image = %q, want an explicit tag", phkgCompose, name, ref)
			continue
		}
		switch tag {
		case "latest", "local":
			t.Errorf("%s service %q image = %q — %q says nothing about which build "+
				"is running on a host that has neither this repo nor a registry",
				phkgCompose, name, ref, tag)
		}
	}
}

// The shared stack's network lives on the production host. Compose resolves
// every DECLARED external network at `up`, whether or not a service still
// attaches to it, so leaving the declaration behind fails the whole deploy
// with "network declared as external, but could not be found" — a message
// that reads as "the shared stack is down" and is not.
func TestPHKGDeployComposeDeclaresNoExternalNetwork(t *testing.T) {
	src := composeSource(t, phkgCompose)
	for i, line := range strings.Split(src, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "external:") {
			t.Errorf("%s line %d declares an external network: %q — nothing outside "+
				"this compose project exists on the deploy host",
				phkgCompose, i+1, strings.TrimSpace(line))
		}
	}
}

// Same claim as TestWaxumPublishesNoHostPort makes about the base file, made
// again here because this is a SEPARATE file that a separate edit can break.
// waxum's SUPERADMIN_TOKEN is a root credential for every WhatsApp session it
// holds, compared in constant time and bypassing JWT entirely; publishing the
// port puts that credential's blast radius one hop from the LAN instead of
// zero.
func TestPHKGDeployComposeKeepsWaxumOffTheHostNetwork(t *testing.T) {
	block, ok := composeServices(t, composeSource(t, phkgCompose))["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + phkgCompose)
	}
	for i, line := range strings.Split(block, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if regexp.MustCompile(`^\s*ports:\s*$`).MatchString(code) ||
			regexp.MustCompile(`^\s*-\s*["']?\$?\{?\w*:?-?[\d.]*\}?:?\d*:\d+`).MatchString(code) {
			t.Errorf("%s waxum service line %d publishes a host port: %q — waxum is "+
				"reachable ONLY at http://waxum:3451 on the compose network",
				phkgCompose, i+1, strings.TrimSpace(line))
		}
	}
}

// Stock waxum cannot send pre-uploaded media at all — it fetches the
// descriptor's encrypted mmg.whatsapp.net blob and delivers an undecryptable
// file while the API answers 200. See TestWaxumImageIsOurForkOnAPinnedTag for
// the mechanism. A deploy bundle that reverted to upstream would ship that
// silent corruption to a host where nobody has the repo to diagnose it.
func TestPHKGDeployComposeUsesOurWaxumFork(t *testing.T) {
	block, ok := composeServices(t, composeSource(t, phkgCompose))["waxum"]
	if !ok {
		t.Fatal(`no "waxum" service found in ` + phkgCompose)
	}
	m := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)\s*$`).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("%s waxum service has no image line at all", phkgCompose)
	}
	if !strings.HasPrefix(m[1], "ardihikaru/waxum:") {
		t.Fatalf("%s waxum image = %q, want ardihikaru/waxum:<pinned tag> — an "+
			"upstream build silently corrupts every media send", phkgCompose, m[1])
	}
}

// Drift guard, the same shape as
// TestRemoteDBOverlayRepointsMySQLForEveryServiceThatJoinsTheSharedNetwork:
// a service added to the base file must not quietly go missing from the
// bundle. The failure mode without this is not an error — it is a deploy that
// comes up looking healthy while one process simply is not there.
func TestPHKGDeployComposeCarriesEveryServiceFromTheBaseFile(t *testing.T) {
	deployed := composeServices(t, composeSource(t, phkgCompose))
	for name := range composeServices(t, composeSource(t, baseCompose)) {
		if _, ok := deployed[name]; !ok {
			t.Errorf("service %q exists in %s but not in %s — a service added to the "+
				"base file has to be carried into the deploy bundle deliberately, "+
				"or dropped deliberately with a comment saying why",
				name, baseCompose, phkgCompose)
		}
	}
}

// The template is committed to git and rsynced to a remote host, so a value
// left in it by accident is a secret published twice over. Every credential
// this deployment needs is generated fresh for it; none is carried over from
// production, where WAXUM_TOKEN in particular is a root credential for every
// session on the production waxum instance.
func TestPHKGEnvTemplateCarriesNoSecretValues(t *testing.T) {
	secretKey := regexp.MustCompile(`(?i)(PASSWORD|SECRET|TOKEN|ACCESS_KEY)`)
	for i, line := range strings.Split(composeSource(t, phkgEnv), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !secretKey.MatchString(key) {
			continue
		}
		// An empty value is the point: docker-compose.yml guards these with
		// `${VAR:?}`, so an unfilled deployment refuses to start instead of
		// booting half-configured. Quotes count as empty — the admin hash
		// carries '' so a bcrypt `$2a$10$…` pasted between them is not
		// interpolated by compose.
		if strings.Trim(strings.TrimSpace(value), `'"`) != "" {
			t.Errorf("%s line %d sets %s to a value — every credential here is "+
				"generated fresh for the deploy host, and this file is committed "+
				"and copied to a remote machine", phkgEnv, i+1, strings.TrimSpace(key))
		}
	}
}

// The temp bucket is the one MinIO setting whose DEFAULT is dangerous on this
// deployment's server. config.DefaultMinioTempBucket is the bare word "temp",
// and since the 2026-09-02 cutover this host's MinIO is 192.168.20.12:9000 —
// shared with other products, where `temp` is an existing, populated bucket
// (19,784 objects when measured at cutover). WSM_MINIO_ACCESS_KEY here is a
// MinIO ADMIN key, so falling back to the default would write WhatsApp temp
// media into another product's bucket using a credential that can also delete
// what is already in it.
//
// EnsureBuckets creates whatever name it is given, so the scoped name costs
// nothing — which is exactly why this is easy to "tidy" back to the default by
// someone comparing the template against config's defaults and seeing
// redundancy. It is not redundant, and the damage would be silent.
func TestPHKGEnvTemplateScopesTheMinioTempBucket(t *testing.T) {
	const key = "WSM_MINIO_TEMP_BUCKET"
	var value string
	var found bool
	for _, line := range strings.Split(composeSource(t, phkgEnv), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			value, found = strings.Trim(strings.TrimSpace(v), `'"`), true
		}
	}
	if !found {
		t.Fatalf("%s does not set %s — unset means config falls back to "+
			"DefaultMinioTempBucket (%q), which is another product's populated "+
			"bucket on this deployment's shared MinIO",
			phkgEnv, key, config.DefaultMinioTempBucket)
	}
	if value == "" || value == config.DefaultMinioTempBucket {
		t.Errorf("%s sets %s=%q, want a product-scoped bucket: %q is the shared "+
			"MinIO's existing temp bucket, and this host's MinIO key is an admin "+
			"key that can delete its contents",
			phkgEnv, key, value, config.DefaultMinioTempBucket)
	}
}

// The bundle is useless without the script that loads the images, the SQL that
// creates the schema, and the runbook that says in which order. Naming them
// here means deleting or renaming one is a test failure, not a discovery made
// on the deploy host.
func TestPHKGBundleIsComplete(t *testing.T) {
	for _, name := range []string{
		"deploy/phkg/docker-compose.yml",
		"deploy/phkg/.env.template",
		"deploy/phkg/load-images.sh",
		"deploy/phkg/bootstrap-db.sql.tmpl",
		"deploy/phkg/README.md",
		// Access control for the published dashboard port. ufw cannot do this
		// job — a Docker-published port never traverses ufw's INPUT chain, so
		// a `ufw deny` reads as applied while the port stays open to everyone.
		// These four are the DOCKER-USER implementation and its installer.
		"deploy/phkg/firewall.sh",
		"deploy/phkg/wa-phkg-firewall.service",
		"deploy/phkg/install-firewall.sh",
		"deploy/phkg/check-firewall.sh",
	} {
		info, err := os.Stat(name)
		if err != nil {
			t.Errorf("%s missing from the deploy bundle: %v", name, err)
			continue
		}
		if strings.HasSuffix(name, ".sh") && info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (%v) — it is run directly on a host with "+
				"no make and no repo", name, info.Mode().Perm())
		}
	}
}

// load-images.sh used to hardcode STAMP=20260901 — a silent way to reload a
// stale build while believing a new one was loaded, since the script would
// still "succeed" against whatever old tars happened to be in images/. It
// also used to load a waxum tar.gz on every transfer even though waxum has
// its own pinned tag and is not part of a normal app/migrate transfer. See
// deploy/phkg/load-images.sh's own header comment for the full story.
func TestPHKGLoadImagesRequiresAnExplicitStampAndLoadsOnlyAppAndMigrate(t *testing.T) {
	src := composeSource(t, "deploy/phkg/load-images.sh")
	if regexp.MustCompile(`(?m)^STAMP=\d+\s*$`).MatchString(src) {
		t.Error("load-images.sh hardcodes a numeric STAMP default — a stale value here " +
			"silently reloads an old build instead of failing; STAMP must come from an " +
			"argument or the environment with no numeric fallback")
	}
	if regexp.MustCompile(`waxum[-\w.]*\.tar\.gz`).MatchString(src) {
		t.Error("load-images.sh still references a waxum *.tar.gz filename — waxum has its own " +
			"pinned tag and is no longer part of a normal app/migrate transfer")
	}
	if !strings.Contains(src, "SHA256SUMS-$STAMP") && !strings.Contains(src, `SHA256SUMS-"$STAMP"`) {
		t.Error(`load-images.sh does not checksum against a stamp-named "SHA256SUMS-<stamp>" ` +
			"file — the checksum file travels under the same stamp as the tars it covers")
	}
}
