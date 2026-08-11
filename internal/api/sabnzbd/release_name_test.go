package sabnzbd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestReleaseNameKeepsGrabbedTitleForSingleTracks(t *testing.T) {
	// SpotiFLAC reports no album for a single track. Overwriting the name that
	// Lidarr sent at addurl time produced "Fred again.., BLANCO - ", which
	// Lidarr could not match against its own grab, so completed singles were
	// treated as untracked downloads and never imported.
	got := releaseName("Fred again.., BLANCO - solo [FLAC]", spotiflac.ProgressEvent{
		Type:   "metadata",
		Artist: "Fred again.., BLANCO",
		Title:  "solo",
	})
	assert.Equal(t, "Fred again.., BLANCO - solo [FLAC]", got)
}

func TestReleaseNamePrefersFullAlbumMetadata(t *testing.T) {
	got := releaseName("stale name", spotiflac.ProgressEvent{
		Type:   "complete",
		Artist: "Eminem",
		Album:  "Kamikaze",
	})
	assert.Equal(t, "Eminem - Kamikaze", got)
}

func TestReleaseNameFallsBackWhenJobHasNoName(t *testing.T) {
	assert.Equal(t, "Eminem - Godzilla", releaseName("", spotiflac.ProgressEvent{
		Artist: "Eminem",
		Title:  "Godzilla",
	}))
	assert.Equal(t, "Eminem", releaseName("", spotiflac.ProgressEvent{Artist: "Eminem"}))
	assert.Equal(t, "Godzilla", releaseName("  ", spotiflac.ProgressEvent{Title: "Godzilla"}))
	assert.Empty(t, releaseName("", spotiflac.ProgressEvent{}))
}
