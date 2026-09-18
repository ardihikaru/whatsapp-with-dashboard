package groupbroadcast

import (
	"context"
	"errors"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// managerGateway is the production Gateway, a thin adapter over
// *wa.Manager — mirrors internal/selfconvo/gateway_wa.go's managerGateway
// exactly, minus the MessageStatus/Recover machinery that package needs and
// this one does not (a group-broadcast turn's own next Tick re-checks
// health before ever re-sending; there is no boot-time mid-send
// reconciliation pass in this package — see runner.go's doc comment).
type managerGateway struct {
	m *wa.Manager
}

// NewManagerGateway wraps m as a Gateway.
func NewManagerGateway(m *wa.Manager) Gateway {
	return &managerGateway{m: m}
}

func (g *managerGateway) Participant(id string) (Participant, bool) {
	s, ok := g.m.Get(id)
	if !ok {
		return Participant{}, false
	}
	snap := s.Snapshot()
	return Participant{ID: snap.ID, Name: snap.Name, Phone: snap.Phone, Connected: snap.Connected}, true
}

// Send builds a message.Message stamped with the group-broadcast category
// and dispatches it through wa.Manager.Dispatch — the SAME
// destination-agnostic send path POST /api/v1/messages/group already uses,
// with req.ToGroupJID passed straight through with no conversion of its
// own. See internal/selfconvo/gateway_wa.go's own Send doc comment for the
// full Dispatch error contract this mirrors.
func (g *managerGateway) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	s, ok := g.m.Get(req.SessionID)
	if !ok {
		return SendReceipt{}, wa.ErrNotFound
	}

	msg := &message.Message{
		ID:        req.MessageID,
		SessionID: s.ID,
		ToPhone:   req.ToGroupJID.String(),
		Body:      req.Text,
		Status:    message.StatusQueued,
		ClientRef: req.OccurrenceID,
		Category:  message.CategoryGroupBroadcast,
	}

	dr, err := g.m.Dispatch(ctx, s, msg, req.ToGroupJID, nil, 0)
	if err != nil {
		return SendReceipt{}, err
	}
	if dr.Result == nil {
		if cause := context.Cause(ctx); cause != nil {
			return SendReceipt{}, cause
		}
		return SendReceipt{}, errors.New("groupbroadcast: dispatch returned no result and no error")
	}
	if dr.Result.Err != nil {
		return SendReceipt{}, dr.Result.Err
	}
	return SendReceipt{MessageID: dr.MsgID, WAMessageID: dr.Result.WAMessageID}, nil
}

// Healthy reports on ids via the SAME Participant call above — see
// Gateway.Healthy's doc comment for exactly which signal this reads (and
// does not).
func (g *managerGateway) Healthy(ids []string) (sessionID, reason string, ok bool) {
	for _, id := range ids {
		p, found := g.Participant(id)
		if !found {
			return id, fmt.Sprintf("session %s not found", id), false
		}
		if !p.Connected {
			return id, fmt.Sprintf("session %s (%s) is not connected", p.Name, id), false
		}
	}
	return "", "", true
}
