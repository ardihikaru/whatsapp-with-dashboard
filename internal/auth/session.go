// Package auth implements the in-memory session store, cookie handling and
// the page/API auth middleware. Login is fake by design (D8): any non-empty
// username+password is accepted, so a Session only ever needs a token,
// username and expiry — no user id, no upstream API token.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const janitorInterval = time.Hour

// Session is one logged-in identity. Sessions live in memory only: a restart
// logs everyone out, which is acceptable for this demo tool.
type Session struct {
	Token     string
	Username  string
	ExpiresAt time.Time
}

// Store is a mutex-guarded token → session map.
type Store struct {
	ttl  time.Duration
	mu   sync.RWMutex
	byID map[string]Session
	stop chan struct{}
}

// NewStore creates the store and starts an hourly janitor that evicts
// expired sessions. Call Close on shutdown.
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		ttl:  ttl,
		byID: make(map[string]Session),
		stop: make(chan struct{}),
	}
	go s.janitor()
	return s
}

// Create mints a new session with a 32-byte crypto/rand token.
func (s *Store) Create(username string) (Session, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Session{}, err
	}
	sess := Session{
		Token:     hex.EncodeToString(buf),
		Username:  username,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Lock()
	s.byID[sess.Token] = sess
	s.mu.Unlock()
	return sess, nil
}

// Get returns the session for a token, lazily dropping it when expired.
func (s *Store) Get(token string) (Session, bool) {
	s.mu.RLock()
	sess, ok := s.byID[token]
	s.mu.RUnlock()
	if !ok {
		return Session{}, false
	}
	if time.Now().After(sess.ExpiresAt) {
		s.Delete(token)
		return Session{}, false
	}
	return sess, true
}

// Delete removes a session (logout / expiry).
func (s *Store) Delete(token string) {
	s.mu.Lock()
	delete(s.byID, token)
	s.mu.Unlock()
}

// Close stops the janitor goroutine.
func (s *Store) Close() {
	close(s.stop)
}

func (s *Store) janitor() {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			for token, sess := range s.byID {
				if now.After(sess.ExpiresAt) {
					delete(s.byID, token)
				}
			}
			s.mu.Unlock()
		}
	}
}
