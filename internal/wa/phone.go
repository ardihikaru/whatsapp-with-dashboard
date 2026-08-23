package wa

import (
	"errors"
	"regexp"
)

var nonDigits = regexp.MustCompile(`\D`)

// NormalizePhone accepts "+62 812-0000-0000" or "6281200000000" and returns
// bare digits. Local format is rejected — these rules mirror whatsmeow's own
// PairPhone validation, so one validator serves pairing, session create, and
// both send paths.
func NormalizePhone(raw string) (string, error) {
	d := nonDigits.ReplaceAllString(raw, "")
	if len(d) <= 6 {
		return "", errors.New("phone number too short — use international format like +62 812-0000-0000")
	}
	if d[0] == '0' {
		return "", errors.New("use international format with country code (no leading 0)")
	}
	return d, nil
}

// ToJID turns normalized digits into "<digits>@s.whatsapp.net".
func ToJID(digits string) JID {
	return JID{User: digits, Server: userServer}
}
