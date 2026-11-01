package statussched

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// ErrNoObjectStore is returned when an image/video schedule fires on a
// deployment with no object storage wired. Saving such a schedule is refused
// up front (the handler), so this only guards a store that went away later.
var ErrNoObjectStore = errors.New("object storage is not configured; cannot load the stored media")

// managerGateway is the production Gateway over *wa.Manager.
type managerGateway struct {
	m        *wa.Manager
	contacts *contact.Repository
	objects  storage.ObjectStore
}

// NewManagerGateway wires the scheduler to the live manager, the contact
// book and (for image/video posts) object storage. contacts and objects may
// be nil; the corresponding schedules then fail with a clear message.
func NewManagerGateway(m *wa.Manager, contacts *contact.Repository, objects storage.ObjectStore) Gateway {
	return &managerGateway{m: m, contacts: contacts, objects: objects}
}

func (g *managerGateway) Connected(sessionID string) (bool, string) {
	s, ok := g.m.Get(sessionID)
	if !ok {
		return false, "session no longer exists"
	}
	if !s.Snapshot().Connected {
		return false, "session not connected"
	}
	return true, ""
}

// ContactJID is the recipient JID for a contact: the stored verified jid,
// else the one derived from the phone — the same rule the /status page's
// checklist uses (handler.statusRecipientOptions).
func ContactJID(c contact.Contact) string {
	if c.JID != "" {
		return c.JID
	}
	return wa.ToJID(c.Phone).String()
}

func (g *managerGateway) ContactJIDs(ctx context.Context) ([]string, error) {
	if g.contacts == nil {
		return nil, errors.New("contact book is not available")
	}
	cs, err := g.contacts.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(cs))
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if j := ContactJID(c); !seen[j] {
			seen[j] = true
			out = append(out, j)
		}
	}
	return out, nil
}

func (g *managerGateway) Post(ctx context.Context, p Post) (string, error) {
	sp, err := g.m.StatusPoster()
	if err != nil {
		return "", err
	}
	var ms wa.MediaStore
	if p.Schedule.Kind != sched.KindText {
		if ms, err = g.m.MediaStore(); err != nil {
			return "", err
		}
	}
	return postVia(ctx, p, sp, ms, g.objects, g.m.LogStatusPost)
}

// postVia publishes p and leaves exactly one wa_messages audit row, success
// or failure, like every other status post (handler.finishStatusPost).
// Media is re-fetched from object storage and re-uploaded on EVERY fire:
// waxum's descriptor lifetime is undocumented, so one is never persisted.
func postVia(ctx context.Context, p Post, sp wa.StatusPoster, ms wa.MediaStore, objects storage.ObjectStore,
	logPost func(ctx context.Context, msg *message.Message, waMessageID string, postErr error) error) (string, error) {
	sc := p.Schedule
	var waID string
	var postErr error
	body, mediaKind := sc.Text, ""

	switch sc.Kind {
	case sched.KindText:
		waID, postErr = sp.PostStatusText(ctx, sc.SessionID, sc.Text, sc.BgARGB, p.Recipients)
	case sched.KindImage, sched.KindVideo:
		body, mediaKind = sc.Caption, sc.Kind
		kind := wa.MediaImage
		if sc.Kind == sched.KindVideo {
			kind = wa.MediaVideo
		}
		var desc wa.MediaDescriptor
		desc, postErr = uploadStored(ctx, sc, kind, ms, objects)
		if postErr == nil {
			if kind == wa.MediaImage {
				waID, postErr = sp.PostStatusImage(ctx, sc.SessionID, desc, sc.Caption, p.Recipients)
			} else {
				waID, postErr = sp.PostStatusVideo(ctx, sc.SessionID, desc, sc.Caption, p.Recipients)
			}
		}
	default:
		postErr = fmt.Errorf("unknown status kind %q", sc.Kind)
	}

	msg := &message.Message{
		ID: wa.NewID(), SessionID: sc.SessionID, ToPhone: "status@broadcast",
		Body: body, Category: message.CategoryManual, MediaKind: mediaKind,
	}
	if logErr := logPost(ctx, msg, waID, postErr); logErr != nil {
		slog.Warn("statussched: could not log wa_messages row (best-effort)", "schedule", sc.ID, "err", logErr)
	}
	return waID, postErr
}

func uploadStored(ctx context.Context, sc sched.Schedule, kind wa.MediaKind, ms wa.MediaStore, objects storage.ObjectStore) (wa.MediaDescriptor, error) {
	if objects == nil {
		return wa.MediaDescriptor{}, ErrNoObjectStore
	}
	if sc.MediaObjectKey == "" {
		return wa.MediaDescriptor{}, errors.New("schedule has no stored media")
	}
	rc, err := objects.Get(ctx, sc.MediaObjectKey)
	if err != nil {
		return wa.MediaDescriptor{}, fmt.Errorf("load stored media: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return wa.MediaDescriptor{}, fmt.Errorf("read stored media: %w", err)
	}
	return ms.UploadMedia(ctx, sc.SessionID, kind, sc.MediaFilename, sc.MediaContentType, data)
}
