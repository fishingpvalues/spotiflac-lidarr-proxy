package sabnzbd

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

const rateLimitMsgTidal = "spotiflac: track Armageddon: Download failed: all requested Tidal qualities failed: failed to get download URL: Tidal community API rate limited (429)"

// TestCooldownForClassifiesUpstreamBackoff pins the distinction the requeue
// path depends on: "upstream told us to wait" (requeue the job) versus "this
// release failed" (fail it). Getting it wrong in either direction is a real
// incident - a mis-classified failure becomes an eternally pending download,
// and a mis-classified cooldown fails a release that was never really tried.
func TestCooldownForClassifiesUpstreamBackoff(t *testing.T) {
	g := newUpstreamBreakGate(zerolog.Nop())

	t.Run("announced break wins with its own duration", func(t *testing.T) {
		d, ok := g.cooldownFor(breakMsgTidal)
		assert.True(t, ok)
		assert.Equal(t, 104*time.Minute, d)
	})

	t.Run("rate limit parks for the configured window", func(t *testing.T) {
		d, ok := g.cooldownFor(rateLimitMsgTidal)
		assert.True(t, ok)
		assert.Equal(t, defaultRateLimitPark, d)
	})

	t.Run("bare 429 with too many requests also counts", func(t *testing.T) {
		d, ok := g.cooldownFor("upstream said 429 Too Many Requests")
		assert.True(t, ok)
		assert.Equal(t, defaultRateLimitPark, d)
	})

	t.Run("a real release failure is not a cooldown", func(t *testing.T) {
		_, ok := g.cooldownFor("partial album: 2/11 tracks")
		assert.False(t, ok)
		_, ok = g.cooldownFor("verification timed out")
		assert.False(t, ok)
	})

	t.Run("longest announced break wins over a shorter one", func(t *testing.T) {
		d, ok := g.cooldownFor("short break, try again in about 5 minutes; short break, try again in about 40 minutes")
		assert.True(t, ok)
		assert.Equal(t, 40*time.Minute, d)
	})
}

func TestRateLimitParkFromEnv(t *testing.T) {
	t.Run("unset falls back to the default", func(t *testing.T) {
		t.Setenv("SPF_RATE_LIMIT_PARK_S", "")
		assert.Equal(t, defaultRateLimitPark, rateLimitParkFromEnv())
	})
	t.Run("garbage falls back to the default", func(t *testing.T) {
		t.Setenv("SPF_RATE_LIMIT_PARK_S", "soon")
		assert.Equal(t, defaultRateLimitPark, rateLimitParkFromEnv())
	})
	t.Run("zero and negative fall back to the default", func(t *testing.T) {
		t.Setenv("SPF_RATE_LIMIT_PARK_S", "0")
		assert.Equal(t, defaultRateLimitPark, rateLimitParkFromEnv())
		t.Setenv("SPF_RATE_LIMIT_PARK_S", "-30")
		assert.Equal(t, defaultRateLimitPark, rateLimitParkFromEnv())
	})
	t.Run("a positive value is honored", func(t *testing.T) {
		t.Setenv("SPF_RATE_LIMIT_PARK_S", "7")
		assert.Equal(t, 7*time.Second, rateLimitParkFromEnv())
	})
}

// TestExtendIsMonotonic guards the rule that a short 429 park must never
// shorten an active long break window.
func TestExtendIsMonotonic(t *testing.T) {
	g := newUpstreamBreakGate(zerolog.Nop())
	assert.True(t, g.extend(time.Hour))
	first := g.remaining()
	assert.False(t, g.extend(time.Minute), "a shorter window must not replace a longer one")
	assert.GreaterOrEqual(t, g.remaining(), first-time.Second)
	assert.False(t, g.extend(0), "a zero cooldown is not a park")
}

// TestRequeueCounterBound pins the bound: three requeues, then the job is
// failed for real. Unbounded requeueing would hide a permanently broken
// release as a download that is forever about to start.
func TestRequeueCounterBound(t *testing.T) {
	h := &Handler{}
	got := []int{}
	for i := 0; i < 5; i++ {
		got = append(got, h.bumpRequeue("SABnzbd_nzo_bound"))
	}
	assert.Equal(t, []int{1, 2, 3, 4, 5}, got)
	assert.Equal(t, 3, maxCooldownRequeues)

	h.clearRequeues("SABnzbd_nzo_bound")
	assert.Equal(t, 1, h.bumpRequeue("SABnzbd_nzo_bound"), "a terminal state must reset the counter")

	// Counters are per job.
	assert.Equal(t, 1, h.bumpRequeue("SABnzbd_nzo_other"))
}
