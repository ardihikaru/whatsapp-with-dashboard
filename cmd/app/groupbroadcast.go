package main

import (
	"context"
	"log"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/aiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// startGroupBroadcast is the boot-time seam for internal/groupbroadcast's
// runner AND planner — hoisted out of run() exactly like startSelfConvo
// (selfconvo.go), so a test can pin the disabled/enabled split without
// booting the whole app. Returns (nil, nil) when cfg.GroupBroadcastEnabled
// is off (config.DefaultGroupBroadcastEnabled); otherwise builds a Runner
// over repo and manager AND a Planner over the same repo/gateway, starts
// both background loops on ctx, and returns them already running — the
// SAME flag gates both, mirroring startSelfConvo's own reasoning (the
// planner only ever produces work by calling CreateOccurrence, which the
// runner then advances).
//
// The AI client is built once here, nil if WSM_CODEX_CLI_* is not
// configured (cfg.CodexCLIURL == "") — the planner runs in fallback-only
// mode without erroring in that case (see groupbroadcast.PlannerOptions.AI's
// own doc comment).
func startGroupBroadcast(ctx context.Context, cfg *config.Config, repo *gb.Repository, manager *wa.Manager) (*groupbroadcast.Runner, *groupbroadcast.Planner) {
	if !cfg.GroupBroadcastEnabled {
		log.Print("group-broadcast: disabled (set WSM_GROUP_BROADCAST_ENABLED=true to enable it)")
		return nil, nil
	}

	var ai *aiclient.Client
	if cfg.CodexCLIURL != "" {
		ai = aiclient.New(cfg.CodexCLIURL, cfg.CodexCLILoginID, cfg.CodexCLISecretKey, cfg.CodexCLITimeout)
	} else {
		log.Print("group-broadcast: WSM_CODEX_CLI_URL not set; running in fallback-only wording mode")
	}

	gw := groupbroadcast.NewManagerGateway(manager)

	runner := groupbroadcast.New(repo, gw, groupbroadcast.RunnerOptions{})
	runner.Start(ctx)
	log.Print("group-broadcast runner started")

	planner := groupbroadcast.NewPlanner(repo, gw, groupbroadcast.PlannerOptions{
		MinSessionGap: cfg.GroupBroadcastMinSessionGap,
		AIDeadline:    cfg.GroupBroadcastAIDeadline,
		AI:            ai,
	})
	planner.Start(ctx)
	log.Print("group-broadcast planner started")
	return runner, planner
}
