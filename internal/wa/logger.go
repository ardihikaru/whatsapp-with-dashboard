package wa

import (
	"fmt"
	"strings"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// sessionLogger keeps whatsmeow's operational logs visible while demoting
// self-healing Signal fanout noise that does not make SendMessage fail.
type sessionLogger struct {
	inner waLog.Logger
}

func newWhatsAppLogger(sessionID string) waLog.Logger {
	return sessionLogger{inner: waLog.Stdout("wa-"+shortSessionID(sessionID), "INFO", true)}
}

func (l sessionLogger) Errorf(msg string, args ...any) { l.inner.Errorf(msg, args...) }
func (l sessionLogger) Infof(msg string, args ...any)  { l.inner.Infof(msg, args...) }
func (l sessionLogger) Debugf(msg string, args ...any) { l.inner.Debugf(msg, args...) }

func (l sessionLogger) Warnf(msg string, args ...any) {
	if isRecoverableSignalFanoutWarning(msg, args...) {
		l.inner.Debugf("%s", fmt.Sprintf(msg, args...))
		return
	}
	l.inner.Warnf(msg, args...)
}

func (l sessionLogger) Sub(module string) waLog.Logger {
	return sessionLogger{inner: l.inner.Sub(module)}
}

func isRecoverableSignalFanoutWarning(msg string, args ...any) bool {
	if strings.HasPrefix(msg, "Failed to encrypt ") {
		return true
	}
	if strings.Contains(msg, "clearing stored identity and retrying") {
		return true
	}
	return false
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
