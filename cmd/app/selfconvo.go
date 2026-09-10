package main

import (
	"context"
	"log"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// startSelfConvo is the boot-time seam for internal/selfconvo's runner —
// hoisted out of run() the same way newBackend is, so main_test.go can pin
// the disabled/enabled split without booting the whole app. Returns nil
// when cfg.SelfConvoEnabled is off (config.DefaultSelfConvoEnabled), which
// run's shutdown sequence treats as "nothing to stop"; otherwise builds a
// Runner over repo and manager, starts its background loop on ctx, and
// returns it already running. msgs is the SAME message repository main.go
// already hands to wa.NewManager — selfconvo.NewManagerGateway needs it too,
// for Recover's MessageStatus reconciliation (internal/selfconvo/gateway_wa.go).
func startSelfConvo(ctx context.Context, cfg *config.Config, repo *convo.Repository, manager *wa.Manager, msgs *message.SQLRepository) *selfconvo.Runner {
	if !cfg.SelfConvoEnabled {
		log.Print("self-convo: disabled (set WSM_SELF_CONVO_ENABLED=true to enable it)")
		return nil
	}
	paraphrase := cfg.SelfConvoParaphrase
	runner := selfconvo.New(repo, selfconvo.NewManagerGateway(manager, msgs), selfconvo.Options{
		Paraphrase: &paraphrase,
	})
	runner.Start(ctx)
	log.Print("self-convo runner started")
	return runner
}
