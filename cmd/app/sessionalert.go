package main

import (
	"context"
	"log"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// startSessionAlert is the boot-time seam for the logged-out alert /
// daily non-connected report loop, hoisted out of run() like
// startStatusSchedules (cmd/app/statussched.go). Always on, no env flag —
// an operator opts in per alert through /settings/alerts; with both off
// the loop only polls an empty/disabled settings row. Started AFTER
// manager.StartAll (main.go's boot order comment) so it never observes
// StartAll's own direct state restoration, only real setState
// transitions published on manager.StateChanges() afterwards — exactly
// the guarantee TestBootRestoreDoesNotPublishAStateChange (internal/wa)
// pins from the publisher's side. It returns the already-running service;
// the caller must Stop it before manager.Close.
func startSessionAlert(ctx context.Context, settings sa.Repository, manager *wa.Manager) *sessionalert.Service {
	svc := sessionalert.New(settings, sessionalert.NewManagerGateway(manager), manager.StateChanges(), sessionalert.Options{})
	svc.Start(ctx)
	log.Print("session-alert service started")
	return svc
}
