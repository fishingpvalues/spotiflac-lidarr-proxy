package indexer_test

import (
	"strconv"
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
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "lossless")
	assert.NoError(t, err)
	assert.NotContains(t, string(xml), `name="size" value="0"`)
}

func TestNewznabXMLIncludesISRCAttrWhenPresent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", ISRC: "USABC1234567"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "lossless")
	require.NoError(t, err)
	assert.Contains(t, string(xml), `name="isrc" value="USABC1234567"`)
}

func TestNewznabXMLOmitsISRCAttrWhenAbsent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "lossless")
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
	out, err := indexer.NewznabXML(results, exampleHost, "test-key", "lossless")
	require.NoError(t, err)
	xml := string(out)
	assert.NotContains(t, xml, `link>https://open.spotify.com`, "link must not point directly at Spotify")
	assert.Contains(t, xml, exampleHost+"/api/newznab?", "download URL must echo back whatever host/base URL it was given, unchanged")
	assert.Contains(t, xml, "t=get", "download URL must point at our own t=get endpoint")
	assert.Contains(t, xml, "id=https%3A%2F%2Fopen.spotify.com%2Falbum%2Fx", "the Spotify URL travels as the id param")
	assert.Contains(t, xml, "apikey=test-key")
}

func TestNewznabXMLTagsTitleWithRecognizableQuality(t *testing.T) {
	// Regression guard: confirmed against a real production Lidarr this
	// session - a release title with no codec/bit-depth token parses as
	// Quality.Unknown (Lidarr's QualityParser), and most quality profiles
	// reject Unknown outright ("Unknown is not wanted in profile"), even
	// after the release is otherwise perfectly valid and grabbable.
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}

	lossless, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "lossless")
	require.NoError(t, err)
	assert.Contains(t, string(lossless), "A - B [FLAC]</title>")

	hires, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "hires")
	require.NoError(t, err)
	assert.Contains(t, string(hires), "A - B [FLAC 24-bit]</title>")
}

func TestNewznabXMLIncludesCategoryAttrs(t *testing.T) {
	// Consumers read item categories only from newznab:attr name="category";
	// the <category> element is decorative. Without the attrs every release
	// parses with an empty category set, so anything that filters or routes on
	// category drops it.
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", TrackCount: 3},
	}

	lossless, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "lossless")
	require.NoError(t, err)
	assert.Contains(t, string(lossless), `name="category" value="3000"`)
	assert.Contains(t, string(lossless), `name="category" value="3010"`,
		"lossless releases must carry the Lossless subcategory advertised in caps")

	hires, err := indexer.NewznabXML(results, "http://localhost:8484", "test-key", "hires")
	require.NoError(t, err)
	assert.Contains(t, string(hires), `name="category" value="3000"`)
	assert.Contains(t, string(hires), `name="category" value="3040"`,
		"hires releases must carry the FLAC 24-bit subcategory advertised in caps")
	assert.NotContains(t, string(hires), `name="category" value="3010"`)
}

func TestNewznabXMLCarriesReleaseNameSizeAndTracksOnTheDownloadURL(t *testing.T) {
	// Lidarr's mode=addfile POST carries no nzbname and no size (verified
	// against a real grab: mode=addfile&cat=music&priority=-100&apikey=...),
	// so the download URL is the only channel for them. t=get folds them
	// into the NZB and the SABnzbd side reads them back - without the name
	// the queue slot marshals as `"filename": ""` and Lidarr never tracks
	// the download at all.
	results := []spotiflac.MetadataResult{
		{Artist: "Daft Punk", Album: "Discovery", SpotifyURL: "https://open.spotify.com/album/x", TrackCount: 14, Year: 2001},
	}
	out, err := indexer.NewznabXML(results, "http://localhost:8484", "k", "lossless")
	require.NoError(t, err)
	xml := string(out)

	assert.Contains(t, xml, "name=Daft+Punk+-+Discovery+%5BFLAC%5D")
	assert.Contains(t, xml, "tracks=14")
	assert.Contains(t, xml, "size="+strconv.FormatInt(indexer.EstimateSizeBytes(14, "lossless"), 10))
	assert.Contains(t, xml, `name="year" value="2001"`)
	assert.Contains(t, xml, `name="files" value="14"`)
}

func TestNewznabXMLOmitsAnUnknownYear(t *testing.T) {
	// A literal 0 is worse than an empty attr: Lidarr parses it as a real
	// release year and then fails its own album-match check on it ("Album
	// match is not close enough: 77.6 % vs 80 % [year, country, tracks]").
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	out, err := indexer.NewznabXML(results, "http://localhost:8484", "k", "lossless")
	require.NoError(t, err)
	assert.Contains(t, string(out), `name="year" value=""`)
	assert.NotContains(t, string(out), `name="year" value="0"`)
}

func TestNewznabXMLCategoryLabelHasNoDanglingSeparator(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	out, err := indexer.NewznabXML(results, "http://localhost:8484", "k", "lossless")
	require.NoError(t, err)
	assert.Contains(t, string(out), "<category>Music</category>")
	assert.NotContains(t, string(out), "<category>Music &gt; </category>")
}

func TestNewznabXMLSizesHiResFromTheHiResPerTrackEstimate(t *testing.T) {
	// The estimate used to be hardcoded to the lossless per-track figure
	// regardless of the requested quality, so a 24-bit release advertised
	// a 16-bit payload size.
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", TrackCount: 10},
	}
	out, err := indexer.NewznabXML(results, "http://localhost:8484", "k", "hires")
	require.NoError(t, err)
	assert.Contains(t, string(out), `name="size" value="`+strconv.FormatInt(indexer.EstimateSizeBytes(10, "hires"), 10)+`"`)
}
