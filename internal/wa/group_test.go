package wa

import (
	"encoding/json"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// GroupInfo is marshalled straight into the API response, so its json tags ARE the
// published contract (openapi.yaml: jid / name / participants). This shipped once
// without tags — Go emitted "JID"/"Name"/"Participants" and any client written from
// the spec would have read nulls, with no error anywhere. Only a live curl caught it.
func TestGroupInfoJSONMatchesTheOpenAPIContract(t *testing.T) {
	b, err := json.Marshal(GroupInfo{JID: "120363@g.us", Name: "Ops", Participants: 4})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"jid", "name", "participants"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %q in %s — openapi.yaml promises it", key, b)
		}
	}
	if len(got) != 3 {
		t.Fatalf("unexpected fields in %s — the contract has exactly 3", b)
	}
}

func TestParseGroupJID(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"full group jid", "120363021234567890@g.us", "120363021234567890@g.us", false},
		{"bare id gets the group server", "120363021234567890", "120363021234567890@g.us", false},
		{"surrounding space", "  120363021234567890@g.us  ", "120363021234567890@g.us", false},

		// THE assertion this endpoint exists for. A user JID here would send the
		// alert to one private chat and return 200: "delivered", and the on-call
		// group never sees it.
		{"user jid is refused", "6281234567890@s.whatsapp.net", "", true},
		{"bare phone-shaped id is NOT special-cased", "6281234567890", "6281234567890@g.us", false},

		{"broadcast is refused", "status@broadcast", "", true},
		{"empty", "", "", true},
		{"garbage", "not-a-jid", "", true},
		{"only a server", "@g.us", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGroupJID(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseGroupJID(%q) = %v, want error", tc.in, got)
				}
				if !errors.Is(err, ErrNotAGroupJID) {
					t.Fatalf("ParseGroupJID(%q) err = %v, want ErrNotAGroupJID", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGroupJID(%q) unexpected error: %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Fatalf("ParseGroupJID(%q) = %q, want %q", tc.in, got.String(), tc.want)
			}
			if got.Server != types.GroupServer {
				t.Fatalf("ParseGroupJID(%q) server = %q, want %q", tc.in, got.Server, types.GroupServer)
			}
		})
	}
}

// A bare all-digit id is read as a group id, so ToJID and ParseGroupJID must
// disagree about the same string. If they ever agreed, one of them would be
// routing messages to the wrong kind of destination.
func TestParseGroupJIDAndToJIDDisagree(t *testing.T) {
	const digits = "6281234567890"
	user := ToJID(digits)
	group, err := ParseGroupJID(digits)
	if err != nil {
		t.Fatalf("ParseGroupJID(%q): %v", digits, err)
	}
	if user.String() == group.String() {
		t.Fatalf("ToJID and ParseGroupJID both produced %q", user.String())
	}
	if user.Server != types.DefaultUserServer || group.Server != types.GroupServer {
		t.Fatalf("servers wrong: user=%q group=%q", user.Server, group.Server)
	}
}
