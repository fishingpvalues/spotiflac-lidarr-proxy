package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

func TestIsValidSpotifyURL(t *testing.T) {
	valid := []string{
		"https://open.spotify.com/album/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/track/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/playlist/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/intl-de/album/4aawyAB9vmqN3uQ7FjRGTy",
	}
	for _, u := range valid {
		assert.True(t, config.IsValidSpotifyURL(u), u)
	}

	invalid := []string{
		"",
		"not-a-url",
		"https://evil.com/album/x",
		"--output-dir",
		"-x",
		"https://open.spotify.com/artist/4aawyAB9vmqN3uQ7FjRGTy", // artist URLs not albums/tracks/playlists
		"https://open.spotify.com/album/../../etc/passwd",
	}
	for _, u := range invalid {
		assert.False(t, config.IsValidSpotifyURL(u), u)
	}
}
