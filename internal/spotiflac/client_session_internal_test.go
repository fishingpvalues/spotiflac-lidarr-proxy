package spotiflac

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSessionFile writes a community session store with the given expiry to
// a temp file and points SPF_COMMUNITY_SESSION_FILE at it.
func writeSessionFile(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "community_session.json")
	content := `{
  "install_id": "test-install-id",
  "session_id": "test-session-id",
  "session_secret": "test-secret",
  "expires_at": "` + expiresAt.Format(time.RFC3339) + `"
}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	t.Setenv("SPF_COMMUNITY_SESSION_FILE", path)
	return path
}

func TestCommunitySessionValid(t *testing.T) {
	now := time.Now()

	t.Run("missing file is not an error", func(t *testing.T) {
		t.Setenv("SPF_COMMUNITY_SESSION_FILE", filepath.Join(t.TempDir(), "does-not-exist.json"))
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "community_session.json")
		require.NoError(t, os.WriteFile(path, []byte("{not json"), 0600))
		t.Setenv("SPF_COMMUNITY_SESSION_FILE", path)
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("missing expires_at", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "community_session.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"install_id":"x"}`), 0600))
		t.Setenv("SPF_COMMUNITY_SESSION_FILE", path)
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("unparseable timestamp", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "community_session.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"expires_at":"yesterday evening"}`), 0600))
		t.Setenv("SPF_COMMUNITY_SESSION_FILE", path)
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("expired", func(t *testing.T) {
		writeSessionFile(t, now.Add(-time.Minute))
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("expiring inside the skew does not count", func(t *testing.T) {
		writeSessionFile(t, now.Add(sessionSkew/2))
		valid, _ := communitySessionValid()
		assert.False(t, valid)
	})

	t.Run("valid session reports its expiry", func(t *testing.T) {
		expiry := now.Add(time.Hour).Truncate(time.Second)
		writeSessionFile(t, expiry)
		valid, got := communitySessionValid()
		assert.True(t, valid)
		assert.Equal(t, expiry, got)
	})
}

func TestSkipPythonPhase(t *testing.T) {
	now := time.Now()

	t.Run("disabled flag never skips", func(t *testing.T) {
		writeSessionFile(t, now.Add(time.Hour))
		c := &Client{} // skipPythonWithSession false
		skip, reason := c.skipPythonPhase("tidal")
		assert.False(t, skip)
		assert.Empty(t, reason)
	})

	t.Run("deezer primary keeps the python phase", func(t *testing.T) {
		writeSessionFile(t, now.Add(time.Hour))
		c := &Client{skipPythonWithSession: true}
		skip, reason := c.skipPythonPhase("deezer")
		assert.False(t, skip)
		assert.Contains(t, reason, "Python-only")
	})

	t.Run("no session file", func(t *testing.T) {
		t.Setenv("SPF_COMMUNITY_SESSION_FILE", filepath.Join(t.TempDir(), "none.json"))
		c := &Client{skipPythonWithSession: true}
		skip, reason := c.skipPythonPhase("tidal")
		assert.False(t, skip)
		assert.Contains(t, reason, "no valid CLI community session")
	})

	t.Run("expired session", func(t *testing.T) {
		writeSessionFile(t, now.Add(-time.Minute))
		c := &Client{skipPythonWithSession: true}
		skip, _ := c.skipPythonPhase("qobuz")
		assert.False(t, skip)
	})

	t.Run("valid session skips for every CLI service", func(t *testing.T) {
		writeSessionFile(t, now.Add(time.Hour))
		c := &Client{skipPythonWithSession: true}
		for _, svc := range cliServices {
			skip, reason := c.skipPythonPhase(svc)
			assert.True(t, skip, "service %s", svc)
			assert.Contains(t, reason, "until")
		}
	})

	t.Run("service match is case-insensitive", func(t *testing.T) {
		writeSessionFile(t, now.Add(time.Hour))
		c := &Client{skipPythonWithSession: true}
		skip, _ := c.skipPythonPhase("TIDAL")
		assert.True(t, skip)
	})
}
