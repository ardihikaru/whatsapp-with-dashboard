// Command e2eseed fills a DEV/E2E SQLite app store with realistic content.
//
// # WHY THIS EXISTS
//
// The browser suite used to run against four empty tables, which quietly made
// its strongest claim meaningless: "no page scrolls horizontally" is trivially
// true when every table has zero rows. Phone layouts break on CONTENT — a long
// message body, a nine-column row, a client name that will not wrap, enough
// rows to render a pager. Seeding is what gives those assertions something to
// bite on.
//
// # SAFETY
//
// This program refuses to run against anything but a "sqlite:" DSN. The only
// MySQL a dev machine can reach is the SHARED production server, and a seeder
// pointed at it would write junk into live tables. The guard is not advisory:
// there is no flag to override it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// ErrNotSQLite is returned for any DSN that is not a local SQLite file.
var ErrNotSQLite = errors.New(
	"e2eseed refuses to run against anything but a sqlite: DSN — the only reachable " +
		"MySQL is the SHARED production server")

// checkDSN is the whole safety story, kept separate so it is directly testable.
func checkDSN(dsn string) error {
	if !strings.HasPrefix(dsn, db.SQLiteAppPrefix) {
		return fmt.Errorf("%w (got %q)", ErrNotSQLite, redact(dsn))
	}
	if strings.TrimPrefix(dsn, db.SQLiteAppPrefix) == "" {
		return fmt.Errorf("%s needs a file path", db.SQLiteAppPrefix)
	}
	return nil
}

// redact keeps a rejected MySQL DSN's password out of the error text.
func redact(dsn string) string {
	if i := strings.LastIndex(dsn, "@"); i >= 0 {
		return "***@" + dsn[i+1:]
	}
	return dsn
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("e2eseed: %v", err)
	}
}

func run() error {
	dsn := os.Getenv("WSM_APP_DSN")
	if err := checkDSN(dsn); err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := db.OpenAppStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	sessions := wasession.NewRepository(pool)
	messages := message.NewRepository(pool)
	clients := apiclient.NewRepository(pool)

	// Idempotent: a second run must not double the rows, because the e2e
	// database persists between runs and duplicated data changes pager counts.
	existing, err := sessions.All(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		log.Printf("already seeded (%d sessions) — nothing to do", len(existing))
		return nil
	}

	// Fixed clock. Nothing here may depend on wall-clock time, or the /logs
	// date filters and the dashboard's "last 30 days" windows would drift and
	// the suite would fail on some days and not others.
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	if err := seedSessions(ctx, sessions, base); err != nil {
		return err
	}
	clientIDs, err := seedClients(ctx, clients)
	if err != nil {
		return err
	}
	n, err := seedMessages(ctx, messages, clientIDs, base)
	if err != nil {
		return err
	}
	log.Printf("seeded %d sessions, %d api clients, %d messages",
		len(seedSessionDefs), len(clientIDs), n)
	return nil
}

// Deliberately awkward content: a name long enough to test truncation, and a
// status mix so the badge colours all render. One session is flagged
// IsMaster: without a seeded master row, the master badge (session_rows.html)
// and the "— master" picker suffix (send.html/calls.html/verify.html) render
// nowhere in the browser suite or any evidence screenshot — see CLAUDE.md's
// "an e2e run against empty tables proves very little".
var seedSessionDefs = []struct {
	ID, Name, Phone, JID, Status string
	Active, IsMaster             bool
}{
	{"e2e-sess-0001", "Nomor Utama Kasir Kemang Selatan", "6285190679041",
		"6285190679041:12@s.whatsapp.net", "connected", true, true},
	{"e2e-sess-0002", "Cadangan Outlet Pondok Indah", "6281234567890",
		"6281234567890:3@s.whatsapp.net", "disconnected", true, false},
	{"e2e-sess-0003", "Sesi Uji Coba", "6289900112233", "", "new", false, false},
}

func seedSessions(ctx context.Context, repo *wasession.SQLRepository, base time.Time) error {
	for i, d := range seedSessionDefs {
		s := &wasession.Session{
			ID: d.ID, Name: d.Name, Phone: d.Phone, JID: d.JID,
			Active: d.Active, IsMaster: d.IsMaster, LastStatus: d.Status,
			CreatedAt: base.Add(-time.Duration(i+1) * 24 * time.Hour),
			UpdatedAt: base,
		}
		if err := repo.Insert(ctx, s); err != nil {
			return fmt.Errorf("insert session %s: %w", d.ID, err)
		}
	}
	return nil
}

// 24 clients: more than the 20-row page size, so the pager actually renders and
// gets laid out on a phone.
func seedClients(ctx context.Context, repo *apiclient.Repository) ([]string, error) {
	ttl := 86400
	names := []string{
		"Kasir Kemang — Integrasi Notifikasi Pesanan Laundry",
		"Backoffice Pondok Indah",
		"OTP Gateway",
	}
	var ids []string
	for i := range 24 {
		name := fmt.Sprintf("Konsumen %02d", i)
		if i < len(names) {
			name = names[i]
		}
		c, err := repo.Create(ctx, name, fmt.Sprintf("e2e-login-%02d", i), "",
			apiclient.TokenSettings{TTLSeconds: &ttl})
		if err != nil {
			return nil, fmt.Errorf("create client %d: %w", i, err)
		}
		ids = append(ids, c.Client.ID)
	}
	return ids, nil
}

// Bodies chosen to stress the layout: a very long one with no early break, one
// with a long unbroken token (a URL), and short ones for contrast.
var seedBodies = []string{
	"Halo kak, cucian Anda dengan nomor order LAUNDRY-2026-08-22-000148 sudah " +
		"selesai dan siap diambil di outlet Kemang Selatan mulai pukul 09.00 " +
		"sampai 21.00. Mohon tunjukkan kode ini saat pengambilan. Terima kasih " +
		"telah menggunakan layanan kami.",
	"Kode OTP Anda 884213. Jangan bagikan kepada siapa pun.",
	"Struk digital: https://wa.mfardiansyah.id/struk/9f2c1b7ae4d84c05a1e6d3f70b8c2a19?ref=LAUNDRY-2026-08-22-000148",
	"Pesanan diterima.",
}

var seedCategories = []string{"notification", "otp", "alert", "manual"}

func seedMessages(
	ctx context.Context, repo *message.SQLRepository, clientIDs []string, base time.Time,
) (int, error) {
	// 45 rows: past the 20-row page size on /logs, and spread across 30 days so
	// the dashboard charts have a real shape instead of a flat line.
	const n = 45
	for i := range n {
		sess := seedSessionDefs[i%2] // only the two paired sessions send
		created := base.Add(-time.Duration(i*16) * time.Hour)
		m := &message.Message{
			ID:          fmt.Sprintf("e2e-msg-%04d", i),
			SessionID:   sess.ID,
			ToPhone:     fmt.Sprintf("62851%08d", 10000000+i),
			Body:        seedBodies[i%len(seedBodies)],
			Category:    seedCategories[i%len(seedCategories)],
			APIClientID: clientIDs[i%len(clientIDs)],
			ClientRef:   fmt.Sprintf("client-uid-%03d", i%7),
			TenantRef:   fmt.Sprintf("tenant-outlet-kemang-selatan-%02d", i%3),
			CreatedAt:   created,
		}
		if err := repo.Insert(ctx, m); err != nil {
			return 0, fmt.Errorf("insert message %d: %w", i, err)
		}
		// A realistic status mix so every badge colour and the failure column
		// are exercised: ~1 in 7 fails, ~1 in 11 stays queued.
		switch {
		case i%7 == 3:
			err := repo.MarkFailed(ctx, m.ID,
				"send timeout after 3 attempts: context deadline exceeded", 3)
			if err != nil {
				return 0, err
			}
		case i%11 == 5:
			// leave queued
		default:
			if err := repo.MarkSent(ctx, m.ID, fmt.Sprintf("3EB0%016X", i), 1); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}
