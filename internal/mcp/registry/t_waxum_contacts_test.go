package registry

import (
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

func strp(s string) *string { return &s }

// TestRankContactsPutsAnExactNameMatchFirst is the task's named ranking
// example applied to rankContacts directly: q="Ria" must rank an
// exact-match contact above a prefix-only one, above a substring-only
// one, across a mix of wsm + waxum sources.
func TestRankContactsPutsAnExactNameMatchFirst(t *testing.T) {
	wsmContacts := []wsmContactWire{
		{ID: "w1", Name: "Maria", Phone: "6281200000001"},
	}
	waxumContacts := []waxum.StoredContact{
		{JID: "6281200000002@s.whatsapp.net", FullName: strp("Garsinia Ely Riani")},
		{JID: "6281200000003@s.whatsapp.net", FullName: strp("Ria")},
	}
	ranked := rankContacts("Ria", wsmContacts, waxumContacts)
	names := make([]string, len(ranked))
	for i, c := range ranked {
		names[i] = c.Name
	}
	if len(names) != 3 || names[0] != "Ria" || names[1] != "Garsinia Ely Riani" || names[2] != "Maria" {
		t.Fatalf("rankContacts names = %v, want [Ria, Garsinia Ely Riani, Maria] in that order", names)
	}
}

// TestRankContactsLetsAWSMOwnerContactOutrankAndDedupeAWaxumDuplicate is
// the task's other named example: a WSM owner contact sharing the SAME
// jid as a synced waxum contact must win the slot (owner contacts win
// ties and dedupe by jid/phone) — the waxum duplicate must not also
// appear in the ranked output.
func TestRankContactsLetsAWSMOwnerContactOutrankAndDedupeAWaxumDuplicate(t *testing.T) {
	wsmContacts := []wsmContactWire{
		{ID: "w1", Name: "Ria", Phone: "6281200000001", JID: "6281200000001@s.whatsapp.net"},
	}
	waxumContacts := []waxum.StoredContact{
		// Same jid, a different (less friendly) name — e.g. only a
		// push_name synced from the device.
		{JID: "6281200000001@s.whatsapp.net", PushName: strp("Ria")},
	}
	ranked := rankContacts("Ria", wsmContacts, waxumContacts)
	if len(ranked) != 1 {
		t.Fatalf("rankContacts = %d results, want exactly 1 (deduped by jid); got %+v", len(ranked), ranked)
	}
	if ranked[0].Source != "wsm" {
		t.Errorf("Source = %q, want %q — the owner contact must win the duplicate slot", ranked[0].Source, "wsm")
	}
}

// TestRankContactsDedupesByPhoneWhenNeitherSideHasAJID covers the
// fallback dedupe key: an unverified wsm contact (no jid yet) must still
// dedupe against a waxum contact sharing the same phone.
func TestRankContactsDedupesByPhoneWhenNeitherSideHasAJID(t *testing.T) {
	wsmContacts := []wsmContactWire{
		{ID: "w1", Name: "Ria", Phone: "6281200000001"},
	}
	waxumContacts := []waxum.StoredContact{
		{Phone: strp("6281200000001"), FullName: strp("Ria Synced")},
	}
	ranked := rankContacts("Ria", wsmContacts, waxumContacts)
	if len(ranked) != 1 {
		t.Fatalf("rankContacts = %d results, want exactly 1 (deduped by phone); got %+v", len(ranked), ranked)
	}
	if ranked[0].Source != "wsm" {
		t.Errorf("Source = %q, want %q", ranked[0].Source, "wsm")
	}
}

// TestRankContactsExcludesCandidatesThatDoNotMatchAtAll pins that
// contactmatch's own exclusion (not every candidate scored 0) applies
// here too.
func TestRankContactsExcludesCandidatesThatDoNotMatchAtAll(t *testing.T) {
	wsmContacts := []wsmContactWire{{ID: "w1", Name: "Totally Unrelated", Phone: "6281200000009"}}
	ranked := rankContacts("Ria", wsmContacts, nil)
	if len(ranked) != 0 {
		t.Fatalf("rankContacts = %+v, want no results for a query sharing nothing with any candidate", ranked)
	}
}

// TestWaxumDisplayNameFallsBackThroughTheFieldOrder pins the documented
// fallback: FullName, then PushName, then FirstName, then BusinessName,
// then Phone.
func TestWaxumDisplayNameFallsBackThroughTheFieldOrder(t *testing.T) {
	cases := []struct {
		name string
		c    waxum.StoredContact
		want string
	}{
		{"full name wins", waxum.StoredContact{FullName: strp("Full"), PushName: strp("Push")}, "Full"},
		{"push name when no full name", waxum.StoredContact{PushName: strp("Push")}, "Push"},
		{"first name when nothing else", waxum.StoredContact{FirstName: strp("First")}, "First"},
		{"business name when nothing else", waxum.StoredContact{BusinessName: strp("Biz")}, "Biz"},
		{"phone as last resort", waxum.StoredContact{Phone: strp("628")}, "628"},
	}
	for _, c := range cases {
		if got := waxumDisplayName(c.c); got != c.want {
			t.Errorf("%s: waxumDisplayName(...) = %q, want %q", c.name, got, c.want)
		}
	}
}
