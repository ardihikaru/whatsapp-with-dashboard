package main

import (
	"context"
	"log"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/aiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/statussched"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// startStatusSchedules is the boot-time seam for the scheduled-status loop,
// hoisted out of run() like startSelfConvo/startGroupBroadcast so a test can
// pin it. Unlike those two there is NO enabling flag: scheduled status posts
// are always on (an operator opts in per schedule by creating one; with no
// schedules the loop only polls an empty table). It returns the already-
// running scheduler; the caller must Stop it before manager.Close.
//
// ai is the shared ai-codex-cli client (buildAIClient), nil when
// WSM_CODEX_CLI_* is not fully configured: schedules with "Write each post
// with AI" then use their static fallback or skip the day, and the form
// refuses to enable the option. deadline bounds one generation.
func startStatusSchedules(ctx context.Context, repo *sched.Repository, manager *wa.Manager,
	contacts *contact.Repository, objects storage.ObjectStore, ai statussched.TextGenerator, deadline time.Duration) *statussched.Scheduler {
	s := statussched.New(repo, statussched.NewManagerGateway(manager, contacts, objects),
		statussched.Options{AI: ai, AIDeadline: deadline})
	s.Start(ctx)
	log.Print("status-schedules scheduler started")
	return s
}

// buildAIClient builds the ai-codex-cli client ONCE for every AI consumer in
// the process, or returns nil when cfg.CodexCLIURL is empty (config.
// loadAIClient only populates it when URL, login and secret are all usable).
func buildAIClient(cfg *config.Config) *aiclient.Client {
	if cfg.CodexCLIURL == "" {
		return nil
	}
	return aiclient.New(cfg.CodexCLIURL, cfg.CodexCLILoginID, cfg.CodexCLISecretKey, cfg.CodexCLITimeout)
}

// statusTextGenerator wraps ai as the scheduler's generator. It returns a
// NIL INTERFACE for a nil client: a typed-nil *aiclient.Client inside a
// non-nil interface would pass the scheduler's "is AI configured" check and
// then panic on first use.
func statusTextGenerator(ai *aiclient.Client, model string) statussched.TextGenerator {
	if ai == nil {
		return nil
	}
	return statussched.ClientGenerator{Client: ai, Model: model}
}
