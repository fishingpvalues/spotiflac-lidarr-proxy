package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

func TestParseCategory(t *testing.T) {
	cases := []struct {
		cat         string
		wantService string
		wantQuality string
	}{
		{"music-tidal", "tidal", ""},
		{"music-qobuz-flac-24", "qobuz", "hires"},
		{"music-flac-16", "", "lossless"},
		{"music-amazon-flac-24", "amazon", "hires"},
		{"MUSIC-TIDAL-FLAC-16", "tidal", "lossless"},
	}
	for _, c := range cases {
		svc, qual := config.ParseCategory(c.cat)
		assert.Equal(t, c.wantService, svc, "service for %s", c.cat)
		assert.Equal(t, c.wantQuality, qual, "quality for %s", c.cat)
	}
}

func TestFallbackServicesParsing(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_FALLBACK_SERVICES", "tidal, qobuz ,amazon")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"tidal", "qobuz", "amazon"}, cfg.FallbackServices)
}

func TestFallbackServicesRejectsInvalidEntry(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_FALLBACK_SERVICES", "tidall,qobuz")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))

	_, err := config.Load()
	require.Error(t, err, "a typo'd service name should be rejected, not silently configured")
	assert.Contains(t, err.Error(), "tidall")
}

func TestFallbackServicesAcceptsAllValidEntries(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_FALLBACK_SERVICES", "tidal,qobuz,amazon,deezer")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"tidal", "qobuz", "amazon", "deezer"}, cfg.FallbackServices)
}

func TestFallbackServicesDefaultEmpty(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))
	// viper reads the process environment (AutomaticEnv), so this test only
	// proves the PROGRAM's default where the variable is genuinely unset.
	// Production exports SPF_FALLBACK_SERVICES, which made this a red test
	// on the very host it matters for.
	t.Setenv("SPF_FALLBACK_SERVICES", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.FallbackServices)
}

// Issue #18: a path-only relay produced a challenge cb=/api/verify-relay
// and a verification error page instead of a callback to this proxy.
func TestVerifyRelayURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		url   string
		valid bool
	}{
		{"unset", "", true},
		{"manual", "http://10.0.0.224:8484/verify/callback", true},
		{"api relay", "https://proxy.example/api/verify-relay", true},
		{"path only", "/api/verify-relay", false},
		{"no scheme", "proxy.example:8484/verify/callback", false},
		{"scheme relative", "//proxy.example/verify/callback", false},
		{"missing host", "http:///verify/callback", false},
		{"wrong scheme", "ftp://proxy.example/verify/callback", false},
		{"credentials", "https://user:secret@proxy.example/verify/callback", false},
		{"fragment", "https://proxy.example/verify/callback#fragment", false},
		{"malformed", "http://[invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPF_API_KEY", "test")
			t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
			t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))
			t.Setenv("SPF_VERIFY_RELAY_URL", tc.url)
			cfg, err := config.Load()
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.url, cfg.VerifyRelayURL)
			} else {
				require.ErrorContains(t, err, "SPF_VERIFY_RELAY_URL must be an absolute http(s) callback URL")
				assert.NotContains(t, err.Error(), "secret")
			}
		})
	}
}
