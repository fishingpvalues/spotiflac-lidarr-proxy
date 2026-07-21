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
