package wa

import "context"

// This file holds waxumBackend's implementation of the ContactDirectory
// capability interface (capabilities.go) — see mapErr (backend_waxum.go)
// for how waxum's 503/429 map onto ErrNotConnected/ErrBackendBusy.

// CheckContacts satisfies ContactDirectory.
func (b *waxumBackend) CheckContacts(ctx context.Context, sessionID string, phones []string) ([]ContactCheck, error) {
	results, err := b.client.CheckContacts(ctx, sessionID, phones)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]ContactCheck, 0, len(results))
	for _, r := range results {
		out = append(out, ContactCheck{Phone: r.Phone, JID: r.JID, IsRegistered: r.IsRegistered})
	}
	return out, nil
}

var _ ContactDirectory = (*waxumBackend)(nil)
