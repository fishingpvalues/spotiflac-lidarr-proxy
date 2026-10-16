package sabnzbd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Error strings copied from production logs, 2026-10-16.
func TestFailureClassesFromProduction(t *testing.T) {
	h := noPythonHandlerWithQueue(t)

	preview := "spotiflac: track Moonlight: detected preview/sample download: file is 30s, expected about 134s. file was removed"
	class, _ := h.classifyFailure(preview)
	assert.Equal(t, failurePermanent, class, "a 30 s preview is the provider's answer for this track")
	assert.False(t, h.shouldRetry(preview))

	conflict := "spotiflac: track Hardwell On Air Intro: Download failed: Amazon API returned status 409"
	class, _ = h.classifyFailure(conflict)
	assert.Equal(t, failurePermanent, class)

	verify := "spotiflac: track My Mind: Download failed: all requested Tidal qualities failed: failed to get download URL: community verification failed: verification timed out"
	class, _ = h.classifyFailure(verify)
	assert.Equal(t, failureRetryable, class, "the breaker must still see a verification wall")
	assert.False(t, h.shouldRetry(verify), "waiting out the same challenge again is pointless")

	dns := `spotiflac: metadata fetch failed: failed to fetch metadata: failed to initialize spotify client: Get "https://open.spotify.com": dial tcp: lookup open.spotify.com on 127.0.0.1:53: read udp 127.0.0.1:41214->127.0.0.1:53: i/o timeout`
	class, d := h.classifyFailure(dns)
	assert.Equal(t, failureUpstreamCooldown, class, "a dead resolver parks the queue instead of failing the job")
	assert.Greater(t, d, time.Duration(0))

	gone := `spotiflac: track Lazy: Download failed: Post "https://amz-oss.spotbye.qzz.io/api/dl": dial tcp: lookup amz-oss.spotbye.qzz.io on 127.0.0.1:53: no such host`
	class, _ = h.classifyFailure(gone)
	assert.Equal(t, failureRetryable, class, "a resolver that answered NXDOMAIN is not a network blip")

	plain := "spotiflac: track X: Download failed: Amazon API returned status 500"
	assert.True(t, h.shouldRetry(plain), "a server error is still worth a retry")
}
