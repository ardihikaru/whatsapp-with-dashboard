package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Defaults. The app now uses TWO stores (hybrid model, docs/07 §2):
//   - MySQL for the app's own tables (wa_sessions, wa_messages, api_clients,
//     api_access_tokens) — see AppDSN below;
//   - SQLite for the whatsmeow sqlstore crypto tables, which whatsmeow can only
//     drive on SQLite or Postgres (no MySQL dialect upstream) — see DBPath.
const (
	DefaultAddr   = ":8800"
	DefaultDBPath = "wsm-store.db" // whatsmeow crypto store ONLY
	// modernc.org/sqlite (pure Go). Was "sqlite3" (mattn, CGO) until Fase 9 —
	// see internal/db.SQLiteDialect for why there is now only one.
	DefaultSQLiteDialect = "sqlite"
	DefaultTTL           = 12 * time.Hour
	DefaultSendRate      = 1.0
	DefaultSendBurst     = 3
	DefaultReconcilePar  = 4

	// MySQL app-store defaults target the SHARED master-compose-ai stack.
	//
	// 127.0.0.1:5306 is the HOST-side mapping of that stack's mysqldb-master
	// (docker-compose.yml there publishes ${MYSQL_EXPOSED_PORT:-5306}:3306).
	// From INSIDE a container on master-compose-ai-net the address is instead
	// mysqldb-master:3306 — our own docker-compose.yml overrides
	// WSM_MYSQL_HOST/PORT for that case, which is why the defaults baked in
	// here are the host-mode values.
	//
	// House rule (one schema + one least-privilege user per product): the app
	// gets its own `wa_dashboard` database and never connects as root.
	DefaultMySQLHost = "127.0.0.1"
	DefaultMySQLPort = "5306"
	DefaultMySQLUser = "wa_dashboard"
	DefaultMySQLPass = "" // no default: a wrong password should fail loudly
	DefaultMySQLDB   = "wa_dashboard"

	// DefaultAdminUser is the login name when WSM_ADMIN_USER is unset. The
	// PASSWORD has no default on purpose — see adminPasswordHash.
	DefaultAdminUser = "admin"

	DefaultBaseURL = "http://localhost:8800"

	// PlatformURL is the sidebar's "back to the parent app" link. Upstream this
	// pointed at the platform backoffice; standalone there is no parent, so the
	// default is EMPTY, which platformLink() renders as "hide the link". Set
	// WA_PLATFORM_URL to an ABSOLUTE url if this ever hangs off something.
	DefaultPlatformURL = ""

	// No proxy is trusted unless one is named. See TrustedProxies.
	DefaultTrustedProxies = ""

	// MinIO: DefaultMinioTempBucket/Timeout/ShareDuration have sensible
	// fallbacks; endpoint/keys/bucket do not (see minioConfig) — a wrong
	// object-storage target should fail loudly, same reasoning as
	// DefaultMySQLPass.
	DefaultMinioTempBucket    = "temp"
	DefaultMinioTimeout       = 30 * time.Second
	DefaultMinioShareDuration = 7 * 24 * time.Hour

	// WA backend selection (see internal/wa/backend.go and
	// internal/wa/backend_waxum.go). Both "whatsmeow" and "waxum" are fully
	// wired (cmd/app/main.go's newBackend). waxum is now the default:
	// docker-compose.yml ships a waxum service on the compose network, so
	// every environment that flips this on also gets WSM_WAXUM_URL/TOKEN set
	// for it (container mode via docker-compose.yml, e2e via
	// scripts/e2e-env.sh overriding back to whatsmeow, since the browser
	// suite never pairs and stays hermetic). WSM_WA_BACKEND=whatsmeow remains
	// a rollback switch — see CLAUDE.md's hazards section for why it is only
	// a full rollback BEFORE a device has paired through waxum.
	DefaultWABackend = "waxum"

	DefaultWaxumTimeout        = 30 * time.Second
	DefaultWaxumQRPollInterval = 2 * time.Second
	DefaultWaxumPollInterval   = 15 * time.Second
	DefaultWaxumPairTimeout    = 180 * time.Second
)

// Config is the resolved application configuration.
// Precedence: environment variables > built-in defaults.
type Config struct {
	Addr          string
	DBPath        string // whatsmeow crypto store (SQLite) path
	AppDSN        string // MySQL DSN for the app tables
	SQLiteDialect string
	SessionTTL    time.Duration
	SendRate      float64
	SendBurst     int
	ReconcilePar  int
	DarkMode      bool

	// AdminUser / AdminPasswordHash are the ONE local operator credential for
	// the browser surface. Upstream this app was an OIDC relying party of a
	// separate auth-service; standalone there is no such service, so the admin
	// comes from the environment. The MACHINE api (api_clients + bearer tokens)
	// is unrelated and untouched — see internal/domain/apiclient.
	AdminUser         string
	AdminPasswordHash []byte // bcrypt; never the plaintext
	// AdminPasswordPlaintext records that the hash was derived from
	// WSM_ADMIN_PASSWORD so main can warn once. Kept as a flag rather than a log
	// call so this package stays free of side effects.
	AdminPasswordPlaintext bool

	BaseURL string
	// BasePath is the URL prefix the BROWSER surface is mounted under, DERIVED
	// from BaseURL rather than configured separately, so "where we are mounted"
	// and "what we tell the world" cannot drift.
	//
	//   https://app.clino.id/wsm  → "/wsm"
	//   http://localhost:8800     → ""
	//
	// The JSON API is deliberately NOT under it — see routes_api.go.
	BasePath     string
	CookieSecure bool

	// PlatformURL is the backoffice link rendered in the sidebar footer ("" hides it).
	PlatformURL string

	// TrustedProxies are the peers whose X-Forwarded-For this service will
	// believe, from WSM_TRUSTED_PROXIES. EMPTY BY DEFAULT, and that default is
	// the safe one: with no proxy named, auth.ClientIP reads the connecting
	// address and nothing else, so the throttle key cannot be spoofed.
	//
	// It matters because the throttles are keyed on that address. Behind a
	// reverse proxy EVERY request arrives from the proxy, so one attacker's
	// failures would spend the budget for every user at once. Naming the proxy
	// here restores per-client keying. Name only proxies that OVERWRITE the
	// header (nginx proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for
	// does); trusting one that merely passes a client's header through is the
	// same as having no throttle at all.
	TrustedProxies []netip.Prefix

	// MinIO object storage. All required (see minioConfig) — the app fails
	// to boot without a working connection, deliberately: there is no
	// consuming feature yet, but a silently-misconfigured connection would
	// only surface the first time something tries to use it.
	MinioEndpoint      string // raw WSM_MINIO_ENDPOINT, kept for logging
	MinioHost          string // host[:port] derived from MinioEndpoint
	MinioSecure        bool   // derived from MinioEndpoint's scheme
	MinioAccessKey     string
	MinioSecretKey     string
	MinioBucket        string
	MinioTempBucket    string
	MinioTimeout       time.Duration
	MinioShareDuration time.Duration

	// WABackend selects the WhatsApp transport: "whatsmeow" (default, the
	// in-process whatsmeow library) or "waxum" (a REST client to a separate
	// waxum process, wa.NewWaxumBackend — see cmd/app/main.go's boot-order
	// comment for why the whatsmeow sqlite store is never opened in this
	// mode).
	WABackend string

	// Waxum* configure the internal/waxum HTTP client. URL/Token are
	// required only when WABackend == "waxum"; the interval/timeout knobs
	// always resolve to a default so they can be read regardless of which
	// backend is selected.
	WaxumURL            string // validated absolute http(s)://host[:port], no trailing slash
	WaxumToken          string
	WaxumTimeout        time.Duration // per-request HTTP timeout
	WaxumQRPollInterval time.Duration
	WaxumPollInterval   time.Duration
	WaxumPairTimeout    time.Duration // hard deadline for the pairing pump
}

// Load resolves the configuration and fails fast on an invalid env value.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:          envOr("WSM_ADDR", DefaultAddr),
		DBPath:        envOr("WSM_DB_PATH", DefaultDBPath),
		AppDSN:        appDSN(),
		SQLiteDialect: envOr("WSM_SQLITE_DIALECT", DefaultSQLiteDialect),
		SessionTTL:    DefaultTTL,
		SendRate:      DefaultSendRate,
		SendBurst:     DefaultSendBurst,
		ReconcilePar:  DefaultReconcilePar,
		BaseURL:       envOr("WA_BASE_URL", DefaultBaseURL),
		PlatformURL:   DefaultPlatformURL,
	}
	bp, err := basePath(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	cfg.BasePath = bp
	cfg.AdminUser = envOr("WSM_ADMIN_USER", DefaultAdminUser)
	hash, plaintext, err := adminPasswordHash()
	if err != nil {
		return nil, err
	}
	cfg.AdminPasswordHash, cfg.AdminPasswordPlaintext = hash, plaintext
	// LookupEnv, not envOr: WA_PLATFORM_URL="" is a meaningful value (hide the link).
	if v, ok := os.LookupEnv("WA_PLATFORM_URL"); ok {
		cfg.PlatformURL = v
	}
	if raw := os.Getenv("WA_COOKIE_SECURE"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid WA_COOKIE_SECURE %q: %v", raw, err)
		}
		cfg.CookieSecure = b
	}
	if raw := os.Getenv("WSM_TRUSTED_PROXIES"); raw != "" {
		prefixes, err := parseTrustedProxies(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid WSM_TRUSTED_PROXIES %q: %v", raw, err)
		}
		cfg.TrustedProxies = prefixes
	}
	if raw := os.Getenv("WSM_SESSION_TTL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid WSM_SESSION_TTL %q: %v", raw, err)
		}
		cfg.SessionTTL = d
	}
	if raw := os.Getenv("WSM_SEND_RATE"); raw != "" {
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || f <= 0 {
			return nil, fmt.Errorf("invalid WSM_SEND_RATE %q: %v", raw, err)
		}
		cfg.SendRate = f
	}
	if raw := os.Getenv("WSM_SEND_BURST"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid WSM_SEND_BURST %q: %v", raw, err)
		}
		cfg.SendBurst = n
	}
	if raw := os.Getenv("WSM_RECONCILE_PAR"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid WSM_RECONCILE_PAR %q: %v", raw, err)
		}
		cfg.ReconcilePar = n
	}
	cfg.DarkMode = true
	if raw := os.Getenv("WSM_DARK_MODE"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid WSM_DARK_MODE %q: %v", raw, err)
		}
		cfg.DarkMode = b
	}
	if err := loadMinio(cfg); err != nil {
		return nil, err
	}
	if err := loadWaxum(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadMinio resolves the MinIO object-storage config. Endpoint/keys/bucket
// have no defaults and are boot failures when unset — see DefaultMinioTempBucket
// for why the other three do.
func loadMinio(cfg *Config) error {
	cfg.MinioEndpoint = os.Getenv("WSM_MINIO_ENDPOINT")
	if cfg.MinioEndpoint == "" {
		return errors.New("WSM_MINIO_ENDPOINT is required")
	}
	host, secure, err := parseMinioEndpoint(cfg.MinioEndpoint)
	if err != nil {
		return err
	}
	cfg.MinioHost, cfg.MinioSecure = host, secure

	cfg.MinioAccessKey = os.Getenv("WSM_MINIO_ACCESS_KEY")
	if cfg.MinioAccessKey == "" {
		return errors.New("WSM_MINIO_ACCESS_KEY is required")
	}
	cfg.MinioSecretKey = os.Getenv("WSM_MINIO_SECRET_KEY")
	if cfg.MinioSecretKey == "" {
		return errors.New("WSM_MINIO_SECRET_KEY is required")
	}
	cfg.MinioBucket = os.Getenv("WSM_MINIO_BUCKET")
	if cfg.MinioBucket == "" {
		return errors.New("WSM_MINIO_BUCKET is required")
	}
	cfg.MinioTempBucket = envOr("WSM_MINIO_TEMP_BUCKET", DefaultMinioTempBucket)

	cfg.MinioTimeout = DefaultMinioTimeout
	if raw := os.Getenv("WSM_MINIO_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_MINIO_TIMEOUT %q: %v", raw, err)
		}
		cfg.MinioTimeout = d
	}
	cfg.MinioShareDuration = DefaultMinioShareDuration
	if raw := os.Getenv("WSM_MINIO_SHARE_DURATION"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_MINIO_SHARE_DURATION %q: %v", raw, err)
		}
		cfg.MinioShareDuration = d
	}
	return nil
}

// parseTrustedProxies reads a comma-separated list of CIDR blocks and bare IP
// addresses into prefixes. A bare address becomes a single-host prefix, so
// "10.10.0.1" and "10.10.0.1/32" mean the same thing and an operator does not
// have to know which form this wants.
//
// A malformed entry is an error rather than a skip: silently dropping one from
// a list would leave the service running with a narrower trust set than the
// operator wrote, which shows up much later as a throttle that keys on the
// proxy for some requests and not others.
func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if strings.Contains(field, "/") {
			p, err := netip.ParsePrefix(field)
			if err != nil {
				return nil, fmt.Errorf("bad CIDR %q: %v", field, err)
			}
			// Masked(): 10.10.0.1/24 is a typo for 10.10.0.0/24, and an
			// unmasked prefix makes Contains behave unpredictably.
			prefixes = append(prefixes, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("bad address %q: %v", field, err)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	if len(prefixes) == 0 {
		return nil, errors.New("no usable entries")
	}
	return prefixes, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// appDSN builds the MySQL DSN for the app tables. WSM_APP_DSN wins verbatim;
// otherwise it is assembled from WSM_MYSQL_* parts. parseTime=true so the
// driver returns time.Time (the repos still store RFC3339 strings, but
// parseTime keeps any native DATETIME reads well-behaved).
func appDSN() string {
	if v := os.Getenv("WSM_APP_DSN"); v != "" {
		return v
	}
	host := envOr("WSM_MYSQL_HOST", DefaultMySQLHost)
	port := envOr("WSM_MYSQL_PORT", DefaultMySQLPort)
	user := envOr("WSM_MYSQL_USER", DefaultMySQLUser)
	pass := envOr("WSM_MYSQL_PASSWORD", DefaultMySQLPass)
	name := envOr("WSM_MYSQL_DATABASE", DefaultMySQLDB)
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=UTC",
		user, pass, host, port, name)
}

// adminPasswordHash resolves the admin password to a bcrypt hash.
//
// WSM_ADMIN_PASSWORD_HASH is the form to prefer: the credential never exists as
// plaintext in the environment, so it does not leak through `docker inspect`, a
// crash dump, or a CI log. WSM_ADMIN_PASSWORD is accepted for local runs and is
// hashed HERE, once, so the verify path downstream is identical either way and
// there is only one comparison to get right.
//
// NEITHER BEING SET IS A BOOT FAILURE, deliberately. A generated-and-logged
// password changes on every restart and writes a live credential into the
// container log; a documented default ships an account takeover to anyone who
// port-forwards this. Failing here costs one error message at a moment when
// someone is watching — and it matches how Load already treats a bad
// WA_BASE_URL or WSM_SESSION_TTL.
func adminPasswordHash() ([]byte, bool, error) {
	if raw := unquote(strings.TrimSpace(os.Getenv("WSM_ADMIN_PASSWORD_HASH"))); raw != "" {
		// bcrypt.Cost parses the digest header; it is the cheapest way to reject
		// a truncated value — or a pasted plaintext password — at boot instead
		// of as a permanently failing login nobody can explain.
		if _, err := bcrypt.Cost([]byte(raw)); err != nil {
			return nil, false, fmt.Errorf("WSM_ADMIN_PASSWORD_HASH is not a bcrypt hash: %w", err)
		}
		return []byte(raw), false, nil
	}
	if raw := os.Getenv("WSM_ADMIN_PASSWORD"); raw != "" {
		if len([]rune(raw)) < 8 {
			return nil, false, errors.New("WSM_ADMIN_PASSWORD must be at least 8 characters")
		}
		// GenerateFromPassword rejects input over 72 bytes (bcrypt's hard limit)
		// rather than silently truncating it.
		h, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
		if err != nil {
			return nil, false, fmt.Errorf("invalid WSM_ADMIN_PASSWORD: %w", err)
		}
		return h, true, nil
	}
	return nil, false, errors.New(
		"no admin credential configured: set WSM_ADMIN_PASSWORD_HASH " +
			"(recommended — generate one with `make hashpw`) or WSM_ADMIN_PASSWORD")
}

// unquote strips one layer of surrounding quotes.
//
// .env holds the bcrypt hash SINGLE-QUOTED, because an unquoted "$2a$10$..."
// is eaten by make's export and by a POSIX shell's `.` alike. docker compose's
// env_file parser strips those quotes for us; `docker run --env-file` does NOT,
// and passes the value through with the quotes still attached — which would
// then fail bcrypt.Cost at boot for a reason nobody would guess from the error.
// Accepting both spellings costs four lines.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
