package main

import (
	"context"
	"log"

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
func startStatusSchedules(ctx context.Context, repo *sched.Repository, manager *wa.Manager,
	contacts *contact.Repository, objects storage.ObjectStore) *statussched.Scheduler {
	s := statussched.New(repo, statussched.NewManagerGateway(manager, contacts, objects), statussched.Options{})
	s.Start(ctx)
	log.Print("status-schedules scheduler started")
	return s
}
