package sessionalert

import (
	"context"
	"errors"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// managerGateway is the production Gateway, a thin adapter over
// *wa.Manager — mirrors internal/groupbroadcast/gateway_wa.go's
// managerGateway.
type managerGateway struct {
	m *wa.Manager
}

// NewManagerGateway wraps m as a Gateway.
func NewManagerGateway(m *wa.Manager) Gateway {
	return &managerGateway{m: m}
}

func (g *managerGateway) Snapshots() []Snapshot {
	snaps := g.m.Snapshots()
	out := make([]Snapshot, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, Snapshot{
			ID: sn.ID, Name: sn.Name, Phone: sn.Phone,
			Connected: sn.Connected, Active: sn.Active,
			State: string(sn.State), StalledReason: sn.StalledReason,
			SelfReminder: sn.SelfReminder,
		})
	}
	return out
}

// Send dispatches text to groupJID through sessionID, stamped with
// message.CategorySessionAlert — the SAME destination-agnostic send path
// internal/groupbroadcast/gateway_wa.go's own Send uses, via
// wa.Manager.Dispatch and wa.ParseGroupJID.
func (g *managerGateway) Send(ctx context.Context, sessionID, groupJID, text string) error {
	s, ok := g.m.Get(sessionID)
	if !ok {
		return wa.ErrNotFound
	}
	jid, err := wa.ParseGroupJID(groupJID)
	if err != nil {
		return fmt.Errorf("sessionalert: invalid target group jid %q: %w", groupJID, err)
	}
	msg := &message.Message{
		ID:        wa.NewID(),
		SessionID: s.ID,
		ToPhone:   jid.String(),
		Body:      text,
		Status:    message.StatusQueued,
		Category:  message.CategorySessionAlert,
	}
	dr, err := g.m.Dispatch(ctx, s, msg, jid, nil, 0)
	if err != nil {
		return err
	}
	if dr.Result == nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return errors.New("sessionalert: dispatch returned no result and no error")
	}
	return dr.Result.Err
}
