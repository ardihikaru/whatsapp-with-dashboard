// WhatsApp Session Management (WSM) — Go + HTMX gateway app for pairing
// WhatsApp device sessions and sending plain-text messages through them.
// Boot order (docs/03): config → SQLite (app schema + whatsmeow sqlstore) →
// repos → wa.Manager + boot reconciliation → HTTP. Shutdown order is a hard
// requirement: server.Shutdown → manager.Close → pool.Close.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/handler"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
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

	// SQLite store: whatsmeow crypto tables only (single writer).
	store, err := db.Open(cfg.DBPath, cfg.SQLiteDialect)
	if err != nil {
		return err
	}
	defer store.Close() // runs LAST: after server drain and manager join
	container := sqlstore.NewWithDB(store, cfg.SQLiteDialect, waLog.Stdout("sqlstore", "INFO", true))
	if err := container.Upgrade(ctx); err != nil { // NewWithDB does NOT auto-upgrade
		return err
	}

	// MySQL store: the app's own tables (schema owned by goose migrations).
	appDB, err := db.OpenApp(cfg.AppDSN)
	if err != nil {
		return err
	}
	defer appDB.Close()

	// One message repo, shared by the send worker (write path) and the /logs +
	// dashboard read path (its analytics methods).
	msgRepo := message.NewRepository(appDB)

	manager := wa.NewManager(ctx, container, wasession.NewRepository(appDB),
		msgRepo, wa.Config{
			SendRate: cfg.SendRate, SendBurst: cfg.SendBurst, ReconcilePar: cfg.ReconcilePar,
		})
	if err := manager.StartAll(ctx); err != nil { // boot reconciliation; never blocks
		return err
	}

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

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: handler.New(handler.Deps{
			Renderer:     rend,
			Sessions:     sessions,
			Middleware:   auth.NewMiddleware(sessions, apiClients, cfg.BasePath),
			Manager:      manager,
			Messages:     msgRepo,
			MessageStats: msgRepo,
			APIClients:   apiClients,
			SessionTTL:   cfg.SessionTTL,
			DarkMode:     cfg.DarkMode,
			PlatformURL:  cfg.PlatformURL,
			AssetsFS:     wsm.FS,
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
		manager.Close()
		return err
	case <-ctx.Done():
		log.Print("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = server.Shutdown(shutdownCtx) // 1) stop accepting / drain HTTP
	manager.Close()                    // 2) close queues, cancel pumps, Disconnect, wg.Wait()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil // 3) deferred pool.Close() runs last
}
