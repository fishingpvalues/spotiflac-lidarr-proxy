package verify

import (
	"sync"
	"time"
)

// Store tracks the most recent pending community-verification request so it
// can be surfaced to an operator (see mode=warnings) and so the callback
// handler has something to validate an inbound request against. Community
// verification is a whole-proxy-instance concern - one shared session file
// on disk, effectively one CLI invocation waiting on it at a time - not a
// per-job one, so a single slot is enough.
type Store struct {
	mu         sync.Mutex
	url        string // display URL for the operator to open
	expectedCB string // the exact upstream_cb the callback handler must see
	at         time.Time
}

func NewStore() *Store {
	return &Store{}
}

// Set records a pending verification: the display URL to show an operator,
// and the exact upstream_cb value spotiflac-cli itself reported expecting a
// grant relayed to. Handler.handleCallback only ever forwards a grant to
// this recorded value, never to whatever an inbound request merely claims
// upstream_cb is - see ExpectedCB.
func (s *Store) Set(url, expectedCB string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.url = url
	s.expectedCB = expectedCB
	s.at = time.Now()
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.url = ""
	s.expectedCB = ""
}

// Pending returns the current display link and when it was set, and whether
// one is set at all.
func (s *Store) Pending() (string, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url, s.at, s.url != ""
}

// ExpectedCB returns the exact upstream_cb value a callback request must
// match to be relayed, and whether one is currently pending at all.
func (s *Store) ExpectedCB() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expectedCB, s.expectedCB != ""
}
