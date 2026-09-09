package selfconvo

import (
	"context"
	"errors"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// managerGateway is the production Gateway, a thin adapter over
// *wa.Manager. It is the only file in this package that imports internal/wa
// — see gateway.go's doc comment for why the runner talks to the Gateway
// interface instead, and runner.go's own doc comment for why it stays that
// way (runner tests must not need a real wa.Manager).
type managerGateway struct {
	m    *wa.Manager
	msgs *message.SQLRepository // for MessageStatus's by-id read; see its own doc comment
}

// NewManagerGateway wraps m as a Gateway. msgs backs MessageStatus (Recover's
// boot-time reconciliation, runner.go) — the same repository main.go already
// builds and hands to wa.NewManager, so this adds no new store.
func NewManagerGateway(m *wa.Manager, msgs *message.SQLRepository) Gateway {
	return &managerGateway{m: m, msgs: msgs}
}

func (g *managerGateway) Participant(id string) (Participant, bool) {
	s, ok := g.m.Get(id)
	if !ok {
		return Participant{}, false
	}
	snap := s.Snapshot()
	return Participant{
		ID:        snap.ID,
		Name:      snap.Name,
		Phone:     snap.Phone,
		Connected: snap.Connected,
	}, true
}

// Send builds a message.Message stamped with the self-convo category and
// the run id (see model.go's CategorySelfConvo doc comment: this is the
// ONLY place that ever stamps it — NormalizeCategory refuses to accept it
// from an untrusted caller), then dispatches it through the SAME audit
// row / send-queue / retry path every other send in this app uses
// (wa.Manager.Dispatch — see that method's doc comment for its full error
// contract, mirrored below).
func (g *managerGateway) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	s, ok := g.m.Get(req.SessionID)
	if !ok {
		return SendReceipt{}, wa.ErrNotFound
	}

	// req.MessageID is minted by the runner and already stored on the turn
	// via ClaimTurn BEFORE this call — see SendRequest.MessageID's doc
	// comment. Using it here (rather than a fresh wa.NewID()) is what keeps
	// the turn and its audit row the same id from before the send starts.
	msg := &message.Message{
		ID:        req.MessageID,
		SessionID: s.ID,
		ToPhone:   req.ToDigits,
		Body:      req.Text,
		Status:    message.StatusQueued,
		ClientRef: req.RunID,
		Category:  message.CategorySelfConvo,
	}

	// wait == 0 blocks until the sender's terminal result or ctx ends —
	// exactly the runner's requirement (docs/brief-self-convo.md's "per-turn
	// send blocks" scheduler decision): turn N+1 must never be sent before
	// turn N is resolved.
	dr, err := g.m.Dispatch(ctx, s, msg, wa.ToJID(req.ToDigits), nil, 0)
	if err != nil {
		// Dispatch's own doc comment: an enqueue-failure sentinel
		// (ErrNotConnected/ErrQueueFull) or the ctx's cause — either way
		// the row (if one was inserted) is already marked failed, and
		// there is nothing terminal to report.
		return SendReceipt{}, err
	}
	if dr.Result == nil {
		// wait == 0 never times out (Dispatch's own contract), so the only
		// way to land here is the ctx ending at the exact instant the
		// terminal result also arrived — Dispatch's ctx.Done() branch
		// races the done channel and can win even on a result that is
		// otherwise ready. Treat it the same as a ctx failure: nothing
		// terminal is known.
		if cause := context.Cause(ctx); cause != nil {
			return SendReceipt{}, cause
		}
		return SendReceipt{}, errors.New("selfconvo: dispatch returned no result and no error")
	}
	if dr.Result.Err != nil {
		return SendReceipt{}, dr.Result.Err
	}
	return SendReceipt{MessageID: dr.MsgID, WAMessageID: dr.Result.WAMessageID}, nil
}

// MessageStatus reads back the wa_messages row messageID names — see
// Gateway.MessageStatus's doc comment for the contract. An empty messageID
// (a turn claimed before this feature existed, or one Recover otherwise has
// no id for) is reported not-found without a query: message.SQLRepository's
// primary key is never empty, so a lookup would only ever waste a round
// trip to learn what is already known.
func (g *managerGateway) MessageStatus(ctx context.Context, messageID string) (status, reason string, found bool, err error) {
	if messageID == "" {
		return "", "", false, nil
	}
	m, err := g.msgs.GetByID(ctx, messageID)
	if errors.Is(err, message.ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return m.Status, m.Error, true, nil
}
