package sabnzbd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

// newHealthExtrasTestHandler builds a handler whose break gate and session
// store are both under test control. The session store is a temp file via
// SPF_COMMUNITY_SESSION_FILE; the break window is set directly on the gate.
func newHealthExtrasTestHandler(t *testing.T) *Handler {
	t.Helper()

	cfg := &config.Config{
		APIKey:         "test-key",
		OutputDir:      t.TempDir(),
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  1,
		JobTimeout:     30 * time.Minute,
	}
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)
	h := NewHandler(q, client, storage.New(cfg.OutputDir), cfg, "0.1.0-test")
	h.SetLogger(zerolog.Nop())
	return h
}

// TestHealthExtrasNoBreakNoSession pins the idle shape of /health's operator
// fields: no upstream break, no community session. Before these fields
// existed a "healthy" container gave no hint that its queue was parked
// behind an upstream break or running on an expired session.
func TestHealthExtrasNoBreakNoSession(t *testing.T) {
	h := newHealthExtrasTestHandler(t)
	t.Setenv("SPF_COMMUNITY_SESSION_FILE", filepath.Join(t.TempDir(), "absent.json"))

	extras := h.HealthExtras()
	assert.Equal(t, int64(0), extras["break_until"], "no break -> break_until 0")
	assert.Nil(t, extras["session_expires_at"], "no session -> session_expires_at nil")
}

// TestHealthExtrasBreakWindowAndSession pins the active shapes: a parked
// window surfaces as an epoch timestamp close to now+window, and a valid
// session's expiry is reported as RFC3339 UTC.
func TestHealthExtrasBreakWindowAndSession(t *testing.T) {
	h := newHealthExtrasTestHandler(t)

	h.breakGate.mu.Lock()
	h.breakGate.until = time.Now().Add(90 * time.Minute)
	h.breakGate.mu.Unlock()

	path := filepath.Join(t.TempDir(), "community_session.json")
	expires := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	content := `{"install_id":"test-install","session_id":"test-session","session_secret":"test-secret","expires_at":"` + expires.Format(time.RFC3339) + `"}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("SPF_COMMUNITY_SESSION_FILE", path)

	extras := h.HealthExtras()
	nu, ok := extras["break_until"].(int64)
	require.True(t, ok, "break_until must be a number, got %T", extras["break_until"])
	want := time.Now().Add(90 * time.Minute).Unix()
	assert.InDelta(t, float64(want), float64(nu), 5, "break_until should be ~now+90m")

	expStr, ok := extras["session_expires_at"].(string)
	require.True(t, ok, "session_expires_at must be a string, got %T", extras["session_expires_at"])
	assert.True(t, strings.HasSuffix(expStr, "Z"), "RFC3339 UTC expiry, got %s", expStr)
	got, err := time.Parse(time.RFC3339, expStr)
	require.NoError(t, err)
	assert.InDelta(t, float64(expires.Unix()), float64(got.Unix()), 2, "expiry should match the written session")
}
