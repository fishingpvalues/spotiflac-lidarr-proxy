package sabnzbd

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

const (
	breakMsgTidal = "spotiflac: track X: Download failed: all requested Tidal qualities failed: failed to get download URL: The server is taking a scheduled short break. Please try again in about 104 minute(s)."
	breakMsgQobuz = "The server is overloaded and taking a short break. Please try again in about 104 minute(s), thanks for your patience."
)

func TestBreakPatternMatchesObservedMessages(t *testing.T) {
	for _, msg := range []string{breakMsgTidal, breakMsgQobuz} {
		m := breakPattern.FindAllStringSubmatch(msg, -1)
		if len(m) != 1 || m[0][1] != "104" {
			t.Fatalf("pattern did not extract 104 from %q (got %v)", msg, m)
		}
	}
}

func TestBreakPatternRejectsUnrelatedErrors(t *testing.T) {
	for _, msg := range []string{
		"",
		"job wall-clock budget (1h0m0s) exhausted: canceled",
		"service tidal temporarily unavailable (circuit open)",
		"track X: Download failed: HTTP 404 not found",
		"the break room is short on coffee", // contains words, no clause shape
	} {
		if m := breakPattern.FindAllStringSubmatch(msg, -1); len(m) != 0 {
			t.Fatalf("pattern matched %q (got %v)", msg, m)
		}
	}
}

func TestRecordExtendsMonotonically(t *testing.T) {
	var now time.Time
	g := newUpstreamBreakGate(zerolog.Nop())
	g.now = func() time.Time { return now }

	if g.record(breakMsgTidal) {
		// first record sets the window
	} else {
		t.Fatal("first record should extend the window")
	}
	want := now.Add(104 * time.Minute)
	if got := g.until; !got.Equal(want) {
		t.Fatalf("until = %v, want %v", got, want)
	}

	now = now.Add(10 * time.Minute)
	// A shorter announcement must not shrink the existing window.
	if g.record("short break. Please try again in about 30 minute(s).") {
		t.Fatal("shorter window must not shrink an existing one")
	}
	if got := g.until; !got.Equal(want) {
		t.Fatalf("until changed to %v, want %v", got, want)
	}

	// A longer announcement extends it.
	if !g.record("short break. Please try again in about 200 minute(s).") {
		t.Fatal("longer window should extend")
	}
	if got := g.until; !got.Equal(now.Add(200 * time.Minute)) {
		t.Fatalf("until = %v, want %v", got, now.Add(200*time.Minute))
	}

	// Non-break errors never touch the window.
	if g.record("plain failure") {
		t.Fatal("non-break error must not extend")
	}
}

func TestWaitReturnsImmediatelyWithoutBreak(t *testing.T) {
	g := newUpstreamBreakGate(zerolog.Nop())
	done := make(chan struct{})
	go func() { g.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wait blocked without an active break")
	}
}

func TestWaitBlocksUntilFakeClockPasses(t *testing.T) {
	var now time.Time
	g := newUpstreamBreakGate(zerolog.Nop())
	g.now = func() time.Time { return now }
	g.poll = 50 * time.Millisecond // don't make the test sleep the production 15 s tick
	g.record(breakMsgTidal)        // until = now + 104m

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		g.wait()
		close(done)
	}()
	<-started

	// Still inside the window: must not return.
	select {
	case <-done:
		t.Fatal("wait returned while the break window is still active")
	case <-time.After(100 * time.Millisecond):
	}

	// Advance the fake clock past the window.
	now = now.Add(105 * time.Minute)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the break window passed")
	}
}
