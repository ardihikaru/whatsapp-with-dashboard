package waxum

import (
	"context"
	"net/http"
)

// ContactCheck is one entry of CheckContacts' response, mirroring waxum's
// own per-phone result field for field.
//
// JID is a plain string, not a pointer, even though waxum's own field is
// Option<String> in Rust: an unregistered number has no JID at all, and
// that case is already distinguishable via IsRegistered without needing a
// nil/non-nil distinction on JID too — a null jid decodes to the empty
// string, and IsRegistered is the field callers must check first.
//
// Phone is the JID's user part as WhatsApp's usync returned it, which can
// differ from what was sent (e.g. a leading 9 dropped by Brazilian mobile
// canonicalisation) — callers matching results back to the phones they
// asked about should not assume this echoes the input verbatim.
type ContactCheck struct {
	Phone        string `json:"phone"`
	JID          string `json:"jid"`
	IsRegistered bool   `json:"is_registered"`
}

// CheckContacts asks waxum whether each of phones (bare digits, no "@"
// suffix — waxum does Jid::pn(s) on each) is a registered WhatsApp number.
func (c *Client) CheckContacts(ctx context.Context, id string, phones []string) ([]ContactCheck, error) {
	body := map[string]any{"phones": phones}
	var out struct {
		Results []ContactCheck `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/contacts/check"), body, &out, true); err != nil {
		return nil, err
	}
	return out.Results, nil
}
