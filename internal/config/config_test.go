package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
