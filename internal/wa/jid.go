package wa

import (
	"fmt"
	"strings"
)

// Known JID servers. Defined locally (rather than imported from whatsmeow)
// because JID is the shared currency between handlers and BOTH backends —
// with two backends, a whatsmeow constant cannot be the thing callers depend
// on either. Values match whatsmeow's types.DefaultUserServer/GroupServer
// exactly; they are WhatsApp protocol constants, not whatsmeow's to own.
const (
	userServer  = "s.whatsapp.net"
	groupServer = "g.us"
)

// JID is this package's own device/group identifier: the type that crosses
// the internal/wa boundary into internal/handler. whatsmeow's types.JID
// carries more (RawAgent, Device, Integrator — an "AD" address, needed to
// address one specific device of a phone number) than any caller outside
// this package reads; the only JIDs handlers ever build are message
// recipients (a phone number or a group id), which never carry those fields.
//
// The session's OWN paired identity (which DOES carry a nonzero Device on a
// linked companion device, and is round-tripped through wa_sessions.jid) is
// deliberately NOT represented by this type — ManagedSession.jid stays
// types.JID (see session.go). Narrowing that one to User/Server would change
// which whatsmeow device row GetDevice finds on reconnect: a real behaviour
// change this phase is not allowed to make.
type JID struct {
	User   string
	Server string
}

// EmptyJID is the zero value: "no destination" / "not yet paired", mirroring
// whatsmeow's types.EmptyJID.
var EmptyJID = JID{}

// String renders "user@server" (or "" for the zero value), matching
// whatsmeow's types.JID.String() for the User/Server-only case this package
// ever constructs.
func (j JID) String() string {
	if j.User == "" && j.Server == "" {
		return ""
	}
	return j.User + "@" + j.Server
}

// IsEmpty reports whether j is the zero value.
func (j JID) IsEmpty() bool { return j == EmptyJID }

// Equal reports whether j and other address the same user/server pair.
func (j JID) Equal(other JID) bool { return j == other }

// ParseJID splits "user@server" as stored in wa_sessions.jid or typed by an
// operator. Deliberately permissive about an empty user (see ParseGroupJID's
// "only a server" case, which relies on that and checks it explicitly) —
// this mirrors whatsmeow's own types.ParseJID, which also accepts "@g.us".
func ParseJID(raw string) (JID, error) {
	user, server, ok := strings.Cut(raw, "@")
	if !ok || server == "" {
		return EmptyJID, fmt.Errorf("wa: invalid jid %q", raw)
	}
	return JID{User: user, Server: server}, nil
}
