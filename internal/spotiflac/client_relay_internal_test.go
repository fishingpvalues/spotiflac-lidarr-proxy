package spotiflac

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveRelayURLExplicitWins(t *testing.T) {
	got := resolveRelayURL("http://tailscale-host:8485/api/verify-relay", "http://fsl:8191", "127.0.0.1", "172.22.0.31", 8485)
	assert.Equal(t, "http://tailscale-host:8485/api/verify-relay", got)
}

func TestResolveRelayURLNoFSLMeansNone(t *testing.T) {
	assert.Empty(t, resolveRelayURL("", "", "172.22.0.31", "172.22.0.31", 8485))
	assert.Empty(t, resolveRelayURL("", "http://fsl:8191", "172.22.0.31", "172.22.0.31", 0))
}

// The regression this exists for: with SPOTIFLAC_ADDRESS=127.0.0.1 (the
// default in the compose file) and FSL running in its own container, the
// auto-constructed relay pointed at our loopback - unreachable from the
// solver, so every grant it delivered was lost and the CLI timed out.
func TestResolveRelayURLLoopbackFallsBackToDetected(t *testing.T) {
	got := resolveRelayURL("", "http://fsl:8191", "127.0.0.1", "172.22.0.31", 8485)
	assert.Equal(t, "http://172.22.0.31:8485/api/verify-relay", got)
}

func TestResolveRelayURLUnsetUsesDetected(t *testing.T) {
	got := resolveRelayURL("", "http://fsl:8191", "", "172.22.0.31", 8485)
	assert.Equal(t, "http://172.22.0.31:8485/api/verify-relay", got)
}

// A non-loopback configured address is used verbatim even when detection
// finds something else - explicit configuration wins over heuristics.
func TestResolveRelayURLNonLoopbackKeptVerbatim(t *testing.T) {
	got := resolveRelayURL("", "http://fsl:8191", "10.0.0.5", "172.22.0.31", 8485)
	assert.Equal(t, "http://10.0.0.5:8485/api/verify-relay", got)
}

// Nothing detected and only loopback configured: keep loopback rather than
// dropping the relay entirely - if the solver shares our namespace (e.g. an
// in-container chromium path) loopback is exactly right.
func TestResolveRelayURLLoopbackWithoutDetectionKept(t *testing.T) {
	got := resolveRelayURL("", "http://fsl:8191", "127.0.0.1", "", 8485)
	assert.Equal(t, "http://127.0.0.1:8485/api/verify-relay", got)
}

func TestIsLoopback(t *testing.T) {
	assert.True(t, isLoopback("127.0.0.1"))
	assert.True(t, isLoopback("127.5.6.7"))
	assert.True(t, isLoopback("::1"))
	assert.False(t, isLoopback("172.22.0.31"))
	assert.False(t, isLoopback("10.0.0.5"))
	assert.False(t, isLoopback(""))
}
