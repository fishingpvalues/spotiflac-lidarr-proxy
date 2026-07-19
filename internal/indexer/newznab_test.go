package indexer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestEstimateSizeBytes(t *testing.T) {
	assert.Equal(t, int64(0), indexer.EstimateSizeBytes(0, "lossless"))
	assert.Greater(t, indexer.EstimateSizeBytes(10, "lossless"), int64(0))
	assert.Greater(t, indexer.EstimateSizeBytes(10, "hires"), indexer.EstimateSizeBytes(10, "lossless"),
		"hires estimate should be larger per track than lossless")
}

func TestNewznabXMLPopulatesNonZeroSize(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", TrackCount: 12},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	assert.NoError(t, err)
	assert.NotContains(t, string(xml), `name="size" value="0"`)
}

func TestNewznabXMLIncludesISRCAttrWhenPresent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", ISRC: "USABC1234567"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	require.NoError(t, err)
	assert.Contains(t, string(xml), `name="isrc" value="USABC1234567"`)
}

func TestNewznabXMLOmitsISRCAttrWhenAbsent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	require.NoError(t, err)
	assert.NotContains(t, string(xml), `name="isrc"`)
}
