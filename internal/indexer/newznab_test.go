package indexer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestCapsXMLDeclaresSupportedSearchParams(t *testing.T) {
	caps := string(indexer.CapsXML("http://localhost:8484", "1.3.2"))
	assert.Contains(t, caps, `<music-search available="yes" supported="yes" supportedParams="q,artist,album" />`,
		"Lidarr only sends artist/album search params an indexer explicitly advertises via supportedParams")
	assert.Contains(t, caps, `<audio-search available="yes" supported="yes" supportedParams="q,artist,album" />`)
}

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
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key")
	assert.NoError(t, err)
	assert.NotContains(t, string(xml), `name="size" value="0"`)
}

func TestNewznabXMLIncludesISRCAttrWhenPresent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", ISRC: "USABC1234567"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key")
	require.NoError(t, err)
	assert.Contains(t, string(xml), `name="isrc" value="USABC1234567"`)
}

func TestNewznabXMLOmitsISRCAttrWhenAbsent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key")
	require.NoError(t, err)
	assert.NotContains(t, string(xml), `name="isrc"`)
}

func TestNewznabXMLEnclosureDownloadsFromOurOwnServer(t *testing.T) {
	// Regression guard: Lidarr fetches the enclosure/link URL itself and
	// requires a well-formed NZB before it will even contact the download
	// client. Pointing it at the raw Spotify page (HTML) fails that check
	// outright - confirmed against a real production Lidarr this session
	// ("Expected 'nzb' found 'html'"). The download URL must be our own
	// t=get endpoint, carrying the Spotify URL as the id param instead.
	//
	// serverURL is deliberately some arbitrary example host, not tied to
	// any specific deployment's actual hostname (production happens to
	// reach this proxy via a VPN sidecar container named "gluetun", but
	// that's just this one caller's value - the real call site derives it
	// per-request from fiber's c.BaseURL(), so any reverse proxy, Docker
	// network alias, or bare hostname:port works identically).
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	const exampleHost = "http://some-example-host:8484"
	out, err := indexer.NewznabXML(results, exampleHost, "test-key")
	require.NoError(t, err)
	xml := string(out)
	assert.NotContains(t, xml, `link>https://open.spotify.com`, "link must not point directly at Spotify")
	assert.Contains(t, xml, exampleHost+"/api/newznab?t=get&amp;id=", "download URL must echo back whatever host/base URL it was given, unchanged")
	assert.Contains(t, xml, "apikey=test-key")
}
