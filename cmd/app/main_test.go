package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// TestNewBackendNeverOpensTheWhatsmeowStoreUnderWaxum is the regression test
// for newBackend's whole reason to exist: selecting WSM_WA_BACKEND=waxum
// must not touch cfg.DBPath at all. db.Open pins MaxOpenConns(1) and takes
// the WAL lock, and on a fresh host it would CREATE the file and write 20
// empty crypto tables into it — an initialised-but-deviceless store that
// looks exactly like "every session's device went missing" to a later
// rollback onto whatsmeow (markDeviceGone, internal/wa/session.go).
func TestNewBackendNeverOpensTheWhatsmeowStoreUnderWaxum(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db") // deliberately not created yet
	cfg := &config.Config{
		WABackend:     "waxum",
		DBPath:        dbPath,
		SQLiteDialect: db.SQLiteDialect,
		WaxumURL:      "http://127.0.0.1:1", // never dialed — NewWaxumBackend does no I/O
		WaxumToken:    "test-token",
	}

	backend, closer, err := newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newBackend(waxum): %v", err)
	}
	if closer != nil {
		t.Fatalf("newBackend(waxum) returned a non-nil io.Closer; waxum mode must open no local store")
	}
	if got := backend.Name(); got != "waxum" {
		t.Fatalf("backend.Name() = %q, want %q", got, "waxum")
	}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Fatalf("newBackend(waxum) created %s — the whatsmeow SQLite store must never be opened in this mode", dbPath)
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("stat %s: %v", dbPath, statErr)
	}
}

// TestNewBackendOpensTheWhatsmeowStoreByDefault is
// TestNewBackendNeverOpensTheWhatsmeowStoreUnderWaxum's mirror: the default
// (unset WABackend, or explicitly "whatsmeow") path must still open and
// upgrade the SQLite crypto store, and hand back a non-nil closer so run
// can defer it — a regression here would leak the SQLite handle (or, worse,
// silently stop upgrading the schema) on every normal boot.
func TestNewBackendOpensTheWhatsmeowStoreByDefault(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db")
	cfg := &config.Config{
		WABackend:     "whatsmeow",
		DBPath:        dbPath,
		SQLiteDialect: db.SQLiteDialect,
	}

	backend, closer, err := newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newBackend(whatsmeow): %v", err)
	}
	t.Cleanup(func() {
		if closer != nil {
			closer.Close()
		}
	})
	if closer == nil {
		t.Fatal("newBackend(whatsmeow) returned a nil io.Closer; run has nothing to defer-close")
	}
	if got := backend.Name(); got != "whatsmeow" {
		t.Fatalf("backend.Name() = %q, want %q", got, "whatsmeow")
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Fatalf("newBackend(whatsmeow) did not create %s: %v", dbPath, statErr)
	}
}
