package groupbroadcast

import (
	"testing"

	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
)

func TestDrawLanguageRespectsExtremeWeights(t *testing.T) {
	rng := newDeterministicRand()
	for i := 0; i < 20; i++ {
		if got := drawLanguage(rng, 100); got != gb.LangID {
			t.Fatalf("drawLanguage(100) = %q, want %q every time", got, gb.LangID)
		}
	}
	for i := 0; i < 20; i++ {
		if got := drawLanguage(rng, 0); got != gb.LangJV {
			t.Fatalf("drawLanguage(0) = %q, want %q every time", got, gb.LangJV)
		}
	}
}

func TestFallbackWordingNeverReturnsAnEmptyString(t *testing.T) {
	rng := newDeterministicRand()
	cases := []wordingInput{
		{Role: gb.RoleSender, Language: gb.LangID, GroupName: "Clino Team", SenderName: "Budi"},
		{Role: gb.RoleSender, Language: gb.LangJV, GroupName: "Clino Team", SenderName: "Budi"},
		{Role: gb.RoleReceiver, Language: gb.LangID, GroupName: "Clino Team", SenderName: "Budi"},
		{Role: gb.RoleReceiver, Language: gb.LangJV, GroupName: "Clino Team", SenderName: "Budi"},
		{Role: gb.RoleReceiver, Language: gb.LangID, IsLate: true, LateReason: "macet", GroupName: "Clino Team", SenderName: "Budi"},
		{Role: gb.RoleReceiver, Language: gb.LangJV, IsLate: true, LateReason: "hujan_deras", GroupName: "Clino Team", SenderName: "Budi"},
	}
	for _, c := range cases {
		got := fallbackWording(c, rng)
		if got == "" {
			t.Errorf("fallbackWording(%+v) returned an empty string", c)
		}
	}
}

func TestPromptForTurnNamesTheGroupAndInstructsAOneLineReply(t *testing.T) {
	in := wordingInput{Role: gb.RoleSender, Language: gb.LangID, GroupName: "Clino Team"}
	got := promptForTurn(in)
	if got == "" {
		t.Fatal("promptForTurn returned an empty string")
	}
	if !contains(got, "Clino Team") {
		t.Errorf("prompt %q does not mention the group name", got)
	}
	if !contains(got, "SATU") {
		t.Errorf("prompt %q does not instruct a single-sentence reply", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
