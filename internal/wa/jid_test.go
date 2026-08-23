package wa

import "testing"

func TestJIDString(t *testing.T) {
	cases := []struct {
		name string
		jid  JID
		want string
	}{
		{"user and server", JID{User: "6281200001111", Server: userServer}, "6281200001111@s.whatsapp.net"},
		{"group jid", JID{User: "120363000000000000", Server: groupServer}, "120363000000000000@g.us"},
		{"zero value renders empty", JID{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.jid.String(); got != tc.want {
				t.Fatalf("JID%+v.String() = %q, want %q", tc.jid, got, tc.want)
			}
		})
	}
}

func TestJIDIsEmpty(t *testing.T) {
	if !(JID{}).IsEmpty() {
		t.Fatal("zero-value JID.IsEmpty() = false, want true")
	}
	if !EmptyJID.IsEmpty() {
		t.Fatal("EmptyJID.IsEmpty() = false, want true")
	}
	if (JID{User: "1", Server: userServer}).IsEmpty() {
		t.Fatal("non-zero JID.IsEmpty() = true, want false")
	}
	// A JID with only one field set is NOT empty — IsEmpty must compare the
	// whole struct, not just User (the field most callers check first).
	if (JID{Server: groupServer}).IsEmpty() {
		t.Fatal("JID with only Server set reported IsEmpty() = true, want false")
	}
}

func TestToJIDBuildsAUserJIDOnTheDefaultServer(t *testing.T) {
	got := ToJID("6281200001111")
	want := JID{User: "6281200001111", Server: userServer}
	if got != want {
		t.Fatalf("ToJID(%q) = %+v, want %+v", "6281200001111", got, want)
	}
	if got.Server != userServer {
		t.Fatalf("ToJID server = %q, want %q — group and user destinations must not collide", got.Server, userServer)
	}
}

// TestParseGroupJIDRejectsAGroupServerWithAnEmptyUser is the specific
// regression this endpoint exists to prevent (see group.go's doc comment):
// ParseJID("@g.us") succeeds with an empty User, and a Server-only assertion
// would let it through to Enqueue — silently addressing nothing (or, with a
// looser backend, some default recipient) while reporting success.
func TestParseGroupJIDRejectsAGroupServerWithAnEmptyUser(t *testing.T) {
	got, err := ParseGroupJID("@g.us")
	if err == nil {
		t.Fatalf("ParseGroupJID(\"@g.us\") = %+v, want error", got)
	}
	if err != ErrNotAGroupJID {
		t.Fatalf("ParseGroupJID(\"@g.us\") err = %v, want ErrNotAGroupJID", err)
	}
}
