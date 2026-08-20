package indexer_test

import (
	"encoding/xml"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
)

func TestGenerateNZBHasNzbRootElement(t *testing.T) {
	// Lidarr's NzbValidationService checks the root element is literally
	// "nzb" before ever contacting the download client - verified against
	// the real error message this session: "Expected 'nzb' found 'html'".
	data, err := indexer.GenerateNZB("https://open.spotify.com/album/x", "Artist - Album", "music-tidal-lossless", 1700000000)
	require.NoError(t, err)

	var probe struct {
		XMLName xml.Name
	}
	require.NoError(t, xml.Unmarshal(data, &probe))
	assert.Equal(t, "nzb", probe.XMLName.Local)
}

func TestGenerateNZBAndExtractRoundTrip(t *testing.T) {
	const url = "https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj"
	data, err := indexer.GenerateNZB(url, "Artist - Album", "music-tidal-lossless", 1700000000)
	require.NoError(t, err)

	got, err := indexer.ExtractSpotifyURLFromNZB(data)
	require.NoError(t, err)
	assert.Equal(t, url, got)
}

func TestExtractSpotifyURLFromNZBErrorsWithoutMeta(t *testing.T) {
	_, err := indexer.ExtractSpotifyURLFromNZB([]byte(`<?xml version="1.0"?><nzb><head></head></nzb>`))
	assert.Error(t, err)
}

func TestExtractSpotifyURLFromNZBErrorsOnGarbage(t *testing.T) {
	_, err := indexer.ExtractSpotifyURLFromNZB([]byte(`not xml at all`))
	assert.Error(t, err)
}

func TestGenerateNZBReleaseRoundTripsNameSizeAndTrackCount(t *testing.T) {
	// These three fields exist purely because Lidarr's mode=addfile request
	// carries none of them, so the NZB is the only channel there is.
	want := indexer.Release{
		SpotifyURL: "https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj",
		Name:       "Daft Punk - Discovery [FLAC]",
		Category:   "music-tidal-lossless",
		Size:       490 * 1024 * 1024,
		TrackCount: 14,
		Date:       1700000000,
	}
	data, err := indexer.GenerateNZBRelease(want)
	require.NoError(t, err)

	got, err := indexer.ParseNZBMeta(data)
	require.NoError(t, err)
	assert.Equal(t, want.SpotifyURL, got.SpotifyURL)
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.Category, got.Category)
	assert.Equal(t, want.Size, got.Size)
	assert.Equal(t, want.TrackCount, got.TrackCount)
}

func TestParseNZBMetaToleratesAnNZBWithoutSizeOrTracks(t *testing.T) {
	data, err := indexer.GenerateNZB("https://open.spotify.com/track/x", "A - B", "", 1700000000)
	require.NoError(t, err)

	got, err := indexer.ParseNZBMeta(data)
	require.NoError(t, err)
	assert.Equal(t, "A - B", got.Name)
	assert.Zero(t, got.Size)
	assert.Zero(t, got.TrackCount)
}
