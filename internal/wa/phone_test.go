package wa

import "testing"

func TestNormalizePhone(t *testing.T) {
	valid := map[string]string{
		"+62 812-0000-0000": "6281200000000",
		"6281200001111":     "6281200001111",
		"+1 (555) 000-1234": "15550001234",
	}
	for in, want := range valid {
		got, err := NormalizePhone(in)
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	invalid := []string{"0812-0000-0000", "12345", "", "abc-def"}
	for _, in := range invalid {
		if got, err := NormalizePhone(in); err == nil {
			t.Errorf("NormalizePhone(%q) = %q, want error", in, got)
		}
	}
}

func TestToJID(t *testing.T) {
	jid := ToJID("6281200001111")
	if jid.String() != "6281200001111@s.whatsapp.net" {
		t.Fatalf("ToJID = %s", jid)
	}
}
