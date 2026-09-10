// WhatsApp Session Management (WSM) — Go + HTMX gateway app for pairing
// WhatsApp device sessions and sending plain-text messages through them.
// Boot order (docs/03): config → MinIO connectivity check → WA backend
// (newBackend: whatsmeow's SQLite crypto store + schema upgrade via
// wa.NewWhatsmeowBackend, OR — when WSM_WA_BACKEND=waxum — a waxum HTTP
// client via wa.NewWaxumBackend with NO SQLite store opened at all; see
// newBackend's doc comment for why that matters) → app store (MySQL/SQLite)
// → repos → wa.Manager + boot reconciliation → self-convo runner AND planner
// (both nil unless WSM_SELF_CONVO_ENABLED=true; startSelfConvo, selfconvo.go)
// → HTTP. Shutdown order is a hard requirement: server.Shutdown →
// planner.Stop (if non-nil) → runner.Stop (if non-nil) → manager.Close
// (which now also closes the backend) → the WA backend's own store, if it
// opened one → pool.Close. The planner stops FIRST, before the runner,
// because the planner's only effect is calling the runner's own
// StartOccurrence — stopping it first guarantees no new occurrence can be
// materialised while the runner is already winding down.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/handler"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

const shutdownTimeout = 5 * time.Second

// displayURL turns a net.Listen-style addr (":8800", "0.0.0.0:8800",
// "127.0.0.1:8800") into a browsable http:// URL for the startup log.
func displayURL(addr string) string {
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	return "http://" + host + ":" + port
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("whatsapp-with-dashboard: %v", err)
	}
}

// newBackend builds the wa.Backend for cfg.WABackend, hoisted out of run so
// the whatsmeow/waxum branch is a unit-testable seam (main_test.go) rather
// than only reachable by booting the whole app.
//
// The two branches are NOT symmetric, and that asymmetry is the reason this
// exists as its own function rather than an inline switch: the whatsmeow
// branch opens db.Open(cfg.DBPath, ...) — a real SQLite file, MaxOpenConns
// pinned to 1, taking the WAL lock — and hands it to the returned
// io.Closer so run can defer its Close at the position db.Open used to
// occupy (boot order is documented above and is a hard requirement). The
// waxum branch opens NOTHING: waxum's Rust process, not this one, owns
// whatever store backs its sessions. Calling db.Open in that branch anyway
// would, on a fresh host, CREATE cfg.DBPath and write 20 empty crypto
// tables into it — an initialised-but-deviceless store that looks exactly
// like "every session's device went missing" to a later rollback onto
// whatsmeow (markDeviceGone, session.go). See
// TestNewBackendNeverOpensTheWhatsmeowStoreUnderWaxum.
func newBackend(ctx context.Context, cfg *config.Config) (wa.Backend, io.Closer, error) {
	switch cfg.WABackend {
	case "waxum":
		client := waxum.New(cfg.WaxumURL, cfg.WaxumToken, cfg.WaxumTimeout)
		backend := wa.NewWaxumBackend(client, wa.WaxumConfig{
			QRPollInterval: cfg.WaxumQRPollInterval,
			PollInterval:   cfg.WaxumPollInterval,
			PairTimeout:    cfg.WaxumPairTimeout,
			EventsEnabled:  cfg.WaxumEventsEnabled,
		})
		return backend, nil, nil
	default: // "whatsmeow" — config.Load already rejects any other value
		store, err := db.Open(cfg.DBPath, cfg.SQLiteDialect)
		if err != nil {
			return nil, nil, err
		}
		backend, err := wa.NewWhatsmeowBackend(ctx, store, cfg.SQLiteDialect)
		if err != nil {
			store.Close()
			return nil, nil, err
		}
		return backend, store, nil
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AdminPasswordPlaintext {
		log.Print("WARNING: WSM_ADMIN_PASSWORD is plaintext in the environment; " +
			"prefer WSM_ADMIN_PASSWORD_HASH (generate one with `make hashpw`)")
	}

	// MinIO: required at boot. No feature consumes it yet, but a
	// misconfigured connection should fail loudly here rather than the first
	// time something tries to use it.
	minioClient, err := storage.New(cfg)
	if err != nil {
		return fmt.Errorf("minio: %w", err)
	}
	minioCtx, minioCancel := context.WithTimeout(ctx, cfg.MinioTimeout)
	err = storage.EnsureBuckets(minioCtx, minioClient, cfg)
	minioCancel()
	if err != nil {
		return fmt.Errorf("minio: %w", err)
	}
	log.Printf("minio: connected to %s (bucket=%s, temp bucket=%s)",
		cfg.MinioEndpoint, cfg.MinioBucket, cfg.MinioTempBucket)
	// Wraps the same minioClient the connectivity check above just proved
	// live — no new connection, just the object-level API (Put/PresignedGet)
	// handlers reach it through. See storage.ObjectStore's doc comment for
	// why this is threaded through Deps as an interface.
	objectStore := storage.NewObjects(minioClient, cfg)

	// WA backend: whatsmeow's SQLite crypto store (single writer), or a
	// waxum HTTP client that opens no local store at all — see newBackend's
	// doc comment for why that split matters.
	backend, waStore, err := newBackend(ctx, cfg)
	if err != nil {
		return err
	}
	if waStore != nil {
		defer waStore.Close() // runs LAST: after server drain and manager join
	}

	// App store: MySQL in production (schema owned by goose migrations), or
	// SQLite when WSM_APP_DSN carries the sqlite: prefix — a dev/e2e escape hatch
	// so the binary boots with no MySQL reachable.
	appDB, err := db.OpenAppStore(ctx, cfg.AppDSN)
	if err != nil {
		return err
	}
	defer appDB.Close()
	if strings.HasPrefix(cfg.AppDSN, db.SQLiteAppPrefix) {
		log.Printf("WARNING: app tables are in SQLite (%s) — dev/e2e only. "+
			"Production runs them in MySQL, where goose owns the schema.", cfg.AppDSN)
	}

	// One message repo, shared by the send worker (write path) and the /logs +
	// dashboard read path (its analytics methods).
	msgRepo := message.NewRepository(appDB)
	callRepo := call.NewRepository(appDB)
	convoRepo := convo.NewRepository(appDB)
	// inboundRepo is nil-able on Manager (see Manager.inbound's doc
	// comment) but always real in production — WSM_INBOUND_ENABLED only
	// gates whether internal/wa's pump actually polls waxum for it, not
	// whether the repository itself is wired.
	inboundRepo := inbound.NewRepository(appDB)

	manager := wa.NewManager(ctx, backend, wasession.NewRepository(appDB),
		msgRepo, inboundRepo, wa.Config{
			SendRate: cfg.SendRate, SendBurst: cfg.SendBurst, ReconcilePar: cfg.ReconcilePar,
			InboundEnabled:      cfg.InboundEnabled,
			InboundPollInterval: cfg.InboundPollInterval,
			InboundStampWindow:  cfg.InboundStampWindow,
		})
	if err := manager.StartAll(ctx); err != nil { // boot reconciliation; never blocks
		return err
	}

	// Self Convo runner + planner: both nil when WSM_SELF_CONVO_ENABLED is
	// off (the default) — see startSelfConvo's doc comment
	// (cmd/app/selfconvo.go).
	runner, planner := startSelfConvo(ctx, cfg, convoRepo, manager, msgRepo)

	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		return err
	}
	rend, err := renderer.New(webFS, cfg.BasePath)
	if err != nil {
		return err
	}
	sessions := auth.NewStore(cfg.SessionTTL)
	defer sessions.Close()
	apiClients := apiclient.NewRepository(appDB)

	// Must precede the server: both throttles below key on auth.ClientIP, and
	// until this is set that function ignores X-Forwarded-For entirely. Empty
	// (the default) leaves it ignoring the header, which is the right answer
	// for a direct listener.
	auth.SetTrustedProxies(cfg.TrustedProxies)
	if len(cfg.TrustedProxies) > 0 {
		log.Printf("trusting X-Forwarded-For from %v", cfg.TrustedProxies)
	}

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: handler.New(handler.Deps{
			Renderer: rend,
			Sessions: sessions,
			// 20 failed Basic attempts, then one more every 3s, per IP. Far
			// looser than the browser login throttle: a machine caller with a
			// stale secret should be slowed to a crawl, not locked out, and the
			// budget only ever moves on FAILURE.
			Middleware: auth.NewMiddleware(sessions, apiClients, cfg.BasePath).
				WithBasicThrottle(auth.NewThrottle(1.0/3.0, 20)),
			Manager:                manager,
			Messages:               msgRepo,
			MessageStats:           msgRepo,
			APIClients:             apiClients,
			Calls:                  callRepo,
			Convo:                  convoRepo,
			SelfConvo:              runner,
			SelfConvoEnabled:       cfg.SelfConvoEnabled,
			Planner:                planner,
			Inbound:                inboundRepo,
			Objects:                objectStore,
			MediaMaxBytes:          cfg.MediaMaxBytes,
			MediaUploadTimeout:     cfg.MediaUploadTimeout,
			MediaUploadConcurrency: cfg.MediaUploadConcurrency,
			MediaArchive:           cfg.MediaArchive,
			MediaMaxBytesImage:     cfg.MediaMaxBytesImage,
			MediaMaxBytesVideo:     cfg.MediaMaxBytesVideo,
			MediaMaxBytesAudio:     cfg.MediaMaxBytesAudio,
			MediaMaxBytesDocument:  cfg.MediaMaxBytesDocument,
			CallsEnabled:           cfg.CallsEnabled,
			SessionTTL:             cfg.SessionTTL,
			DarkMode:               cfg.DarkMode,
			PlatformURL:            cfg.PlatformURL,
			AssetsFS:               wsm.FS,
			Admin: auth.AdminCredential{
				Username:     cfg.AdminUser,
				PasswordHash: cfg.AdminPasswordHash,
			},
			// 5 attempts, then one more every 12s, per client IP.
			LoginThrottle: auth.NewThrottle(1.0/12.0, 5),
			CookieSecure:  cfg.CookieSecure,
			BasePath:      cfg.BasePath,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (docs at /docs)", displayURL(cfg.Addr))
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if planner != nil {
			planner.Stop()
		}
		if runner != nil {
			runner.Stop()
		}
		manager.Close()
		return err
	case <-ctx.Done():
		log.Print("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = server.Shutdown(shutdownCtx) // 1) stop accepting / drain HTTP
	if planner != nil {
		planner.Stop() // 2) join the planner loop FIRST — see this file's own boot-order comment
	}
	if runner != nil {
		runner.Stop() // 3) join the self-convo loop, letting any in-flight send finish
	}
	manager.Close() // 4) close queues, cancel pumps, Disconnect, wg.Wait()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil // 4) deferred pool.Close() runs last
}
