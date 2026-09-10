// Package breaker implements a simple per-key consecutive-failure circuit
// breaker, in-memory only (process lifetime, no persistence).
package breaker

import (
	"sync"
	"time"
)

// State is the externally-visible status of one key's breaker.
type State struct {
	Open                bool
	ConsecutiveFailures int
	OpenedAt            time.Time
	RetryAt             time.Time
}

type entry struct {
	consecutiveFailures int
	openedAt            time.Time
}

// Breaker trips for a key after `threshold` consecutive failures and stays
// open for `cooldown` before allowing traffic again.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	entries   map[string]*entry
}

func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		entries:   make(map[string]*entry),
	}
}

// Allow reports whether a new attempt for key should proceed.
func (b *Breaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok || e.consecutiveFailures < b.threshold {
		return true
	}
	if time.Since(e.openedAt) >= b.cooldown {
		// Cooldown elapsed: allow a trial attempt, reset failure count
		// optimistically. A subsequent RecordFailure will re-open it.
		e.consecutiveFailures = 0
		return true
	}
	return false
}

// RetryAfter reports how long the caller must wait before key is allowed
// again. It returns 0 when the key is allowed right now, so a caller can
// use it as "is this open, and for how long".
//
// This exists so an open circuit can PARK a job instead of failing it. A
// breaker protects the upstream; failing the work it was protecting turns a
// ten-minute provider hiccup into a permanent Lidarr failure. Measured
// 2026-09-10: 151 of 161 proxy failures in the retained history carried
// "circuit open" as their whole error - no download had been attempted.
func (b *Breaker) RetryAfter(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok || e.consecutiveFailures < b.threshold {
		return 0
	}
	remaining := b.cooldown - time.Since(e.openedAt)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

func (b *Breaker) RecordFailure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok {
		e = &entry{}
		b.entries[key] = e
	}
	e.consecutiveFailures++
	if e.consecutiveFailures == b.threshold {
		e.openedAt = time.Now()
	}
}

func (b *Breaker) RecordSuccess(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if e, ok := b.entries[key]; ok {
		e.consecutiveFailures = 0
	}
}

// Status returns a snapshot of every key's breaker state seen so far.
func (b *Breaker) Status() map[string]State {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[string]State, len(b.entries))
	for key, e := range b.entries {
		open := e.consecutiveFailures >= b.threshold && time.Since(e.openedAt) < b.cooldown
		out[key] = State{
			Open:                open,
			ConsecutiveFailures: e.consecutiveFailures,
			OpenedAt:            e.openedAt,
			RetryAt:             e.openedAt.Add(b.cooldown),
		}
	}
	return out
}
