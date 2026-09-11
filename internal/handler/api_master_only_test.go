package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestTheSendDecodersReadMasterOnlyFromTheBody mirrors the allow_master decoder
// tests in api_master_test.go for the new master_only flag. Both endpoints
// matter: wa-notify.sh uses /messages/group when a group JID is configured and
// falls back to /messages when only a phone number is, and an alert that
// silently loses its pinning on the fallback path is the failure this flag
// exists to prevent.
func TestTheSendDecodersReadMasterOnlyFromTheBody(t *testing.T) {
	t.Run("group true", func(t *testing.T) {
		body := `{"group_jid":"120363021234567890@g.us","text":"hi","master_only":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, _, ok := decodeGroupSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeGroupSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if !got.Route.MasterOnly {
			t.Error("MasterOnly = false, want true")
		}
	})
	t.Run("group default false", func(t *testing.T) {
		body := `{"group_jid":"120363021234567890@g.us","text":"hi"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, _, ok := decodeGroupSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeGroupSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if got.Route.MasterOnly {
			t.Error("MasterOnly = true, want false when omitted — every existing caller " +
				"must keep auto-routing unchanged")
		}
	})
	t.Run("text true", func(t *testing.T) {
		body := `{"to":"+62 812-0000-0000","text":"hi","master_only":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if !got.Route.MasterOnly {
			t.Error("MasterOnly = false, want true")
		}
	})
	t.Run("text default false", func(t *testing.T) {
		body := `{"to":"+62 812-0000-0000","text":"hi"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if got.Route.MasterOnly {
			t.Error("MasterOnly = true, want false when omitted")
		}
	})
}

// TestNoSendRouteMessageNamesTheRealCause pins the 409 wording per mode. With
// master_only the gateway may be entirely healthy — three other sessions live —
// so answering "no connected session available" would send an operator hunting
// a gateway outage that is not happening. That is the 2026-09-11 production
// state exactly, and the message must point at the offline master instead.
func TestNoSendRouteMessageNamesTheRealCause(t *testing.T) {
	healthyGatewayOfflineMaster := []wa.Snapshot{
		{ID: "a", Connected: true},
		{ID: "m", Connected: false, IsMaster: true},
	}

	got := noSendRouteMessage(healthyGatewayOfflineMaster, wa.Route{MasterOnly: true})
	if got != noMasterRouteMessage {
		t.Fatalf("master_only 409 = %q, want the master-specific body", got)
	}
	if !strings.Contains(got, "master") {
		t.Errorf("master_only 409 does not mention the master: %q", got)
	}

	// Not master_only: the pre-existing wording, unchanged.
	if got := noSendRouteMessage(healthyGatewayOfflineMaster, wa.Route{}); got != noRouteMessage {
		t.Fatalf("auto-route 409 = %q, want the unchanged %q", got, noRouteMessage)
	}
}
