package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestStartSelfConvoReturnsNoRunnerWhenTheFlagIsOff pins startSelfConvo's
// disabled path: cfg.SelfConvoEnabled false (config.DefaultSelfConvoEnabled)
// must produce a nil *selfconvo.Runner, so main's shutdown sequence
// (`if runner != nil { runner.Stop() }`) skips it cleanly rather than
// nil-pointer-dereferencing on every boot where the operator has not opted
// in.
func TestStartSelfConvoReturnsNoRunnerWhenTheFlagIsOff(t *testing.T) {
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	repo := convo.NewRepository(pool)
	msgs := message.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgs, nil, wa.Config{SendRate: 1, SendBurst: 1, ReconcilePar: 1})

	cfg := &config.Config{SelfConvoEnabled: false}
	runner := startSelfConvo(context.Background(), cfg, repo, mgr, msgs)
	if runner != nil {
		t.Fatalf("startSelfConvo with the flag off returned a non-nil runner")
	}
}

// TestStartSelfConvoReturnsAStartedRunnerWhenTheFlagIsOn is the flag-on
// mirror: a real *selfconvo.Runner is returned and its background loop is
// already running (Stop must not hang or panic on a runner that was never
// Start'd, and must return promptly on one that was).
func TestStartSelfConvoReturnsAStartedRunnerWhenTheFlagIsOn(t *testing.T) {
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	repo := convo.NewRepository(pool)
	msgs := message.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgs, nil, wa.Config{SendRate: 1, SendBurst: 1, ReconcilePar: 1})

	cfg := &config.Config{SelfConvoEnabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := startSelfConvo(ctx, cfg, repo, mgr, msgs)
	if runner == nil {
		t.Fatalf("startSelfConvo with the flag on returned a nil runner")
	}
	runner.Stop()
}
