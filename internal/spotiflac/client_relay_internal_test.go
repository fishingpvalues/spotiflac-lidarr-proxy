package spotiflac

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestInDockerBridgeSpace(t *testing.T) {
	cases := map[string]bool{
		"172.16.0.1":   true,
		"172.22.0.31":  true,
		"172.31.255.1": true,
		"172.15.0.1":   false,
		"172.32.0.1":   false,
		"10.0.0.1":     false,
		"192.168.1.1":  false,
		"8.8.8.8":      false,
		"100.64.0.1":   false, // Tailscale CGNAT - not RFC1918
	}
	for ip, want := range cases {
		assert.Equal(t, want, inDockerBridgeSpace(net.ParseIP(ip)), ip)
	}
}

// Smoke test: whatever this machine reports, it must be empty or a valid
// non-loopback IP - never a network prefix like the old fib-trie scan
// returned (172.16.0.0).
func TestAutoDetectIPReturnsHostAddress(t *testing.T) {
	got := autoDetectIP()
	if got == "" {
		t.Skip("no private interface address on this host")
	}
	ip := net.ParseIP(got)
	require.NotNil(t, ip, "parsed %q", got)
	require.True(t, ip.IsPrivate(), "expected private IP, got %s", got)
	// A route prefix like 172.16.0.0 (what the old fib-trie scan returned)
	// has an all-zero host part; a real interface address should not.
	v4 := ip.To4()
	require.NotNil(t, v4)
	if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
		assert.NotEqual(t, []byte{0, 0, 0, 0}, v4[2:], "host part should not be all zeros for %s", got)
	}
}
