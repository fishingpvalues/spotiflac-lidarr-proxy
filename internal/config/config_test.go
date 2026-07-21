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

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.FallbackServices)
}
