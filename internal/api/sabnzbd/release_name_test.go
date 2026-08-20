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

func TestReleaseNameNeverOverwritesTheGrabbedTitle(t *testing.T) {
	// Whatever Lidarr grabbed is what Lidarr matches on, album metadata
	// included. Renaming the job to "Eminem - Kamikaze" loses the quality
	// tag and the edition Lidarr actually chose, and its tracked download
	// stops matching.
	got := releaseName("Eminem - Kamikaze (Deluxe) [FLAC]", spotiflac.ProgressEvent{
		Type:   "complete",
		Artist: "Eminem",
		Album:  "Kamikaze",
	})
	assert.Equal(t, "Eminem - Kamikaze (Deluxe) [FLAC]", got)
}

func TestReleaseNameUsesAlbumMetadataForAnUnnamedJob(t *testing.T) {
	got := releaseName("", spotiflac.ProgressEvent{
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
