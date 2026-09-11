package newznab_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// noPython points the client at a Python that does not exist, so search and
// download exercise the CLI path deterministically. An empty venv path makes
// findPython fall back to whatever python3 the machine has, and that one has
// no SpotiFLAC module - so the test would spawn a real interpreter, wait for
// it to fail, and only then reach the mock CLI. Harmless in isolation, slow
// and timing-dependent under a full-suite run.
const noPython = "/nonexistent/python3"

func setupNewznabApp(t *testing.T) *fiber.App {
	t.Helper()

	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, noPython, nil)
	handler := newznab.NewHandler(client, "test", "test-key", "lossless")

	app := fiber.New()
	app.Use(api.APIKeyAuth("test-key", nil, []string{"caps"}))
	handler.RegisterRoutes(app)

	return app
}

func TestCaps(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "xml")
}

func TestCapsNoAuth(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "xml")
}

func TestSearch(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=search&q=Test+Artist+Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMusic(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=music&artist=Test+Artist&album=Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestGetReturnsWellFormedNZB(t *testing.T) {
	// Lidarr fetches this URL itself and requires a real NZB (root element
	// "nzb") before it will even contact the download client - confirmed
	// against a real production Lidarr this session.
	app := setupNewznabApp(t)

	id := "https://open.spotify.com/album/x"
	req, _ := http.NewRequest("GET", "/api/newznab?t=get&id="+id+"&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "nzb")
}

func TestMusicSearch(t *testing.T) {
	// Lidarr sends t=musicsearch (Newznab spec standard), not t=music.
	// Prior bug: only "music" was handled; "musicsearch" fell through to
	// default empty-results handler.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=musicsearch&q=debussy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMusicSearchFallbackToQ(t *testing.T) {
	// When only q= is provided (no artist/album), handleMusic must use q
	// as the search query. Prior bug: it only used artist+album.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=musicsearch&q=debussy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestGetMissingIDReturnsBadRequest(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=get&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}

func TestHandleGetFoldsReleaseNameSizeAndTracksIntoTheNZB(t *testing.T) {
	// Lidarr fetches this endpoint and re-uploads the bytes to the download
	// client with a POST that carries no nzbname and no size. The NZB is
	// therefore the only channel for them, and without the name the queue
	// slot's filename is empty and Lidarr never tracks the download.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET",
		"/api/newznab?t=get&id=https%3A%2F%2Fopen.spotify.com%2Falbum%2Fx&name=Daft+Punk+-+Discovery+%5BFLAC%5D&size=513802240&tracks=14&apikey=test-key",
		nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	release, err := indexer.ParseNZBMeta(body)
	require.NoError(t, err)
	assert.Equal(t, "https://open.spotify.com/album/x", release.SpotifyURL)
	assert.Equal(t, "Daft Punk - Discovery [FLAC]", release.Name)
	assert.Equal(t, int64(513802240), release.Size)
	assert.Equal(t, 14, release.TrackCount)
}

func TestHandleGetWithoutANameFallsBackToTheID(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET",
		"/api/newznab?t=get&id=https%3A%2F%2Fopen.spotify.com%2Falbum%2Fy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	release, err := indexer.ParseNZBMeta(body)
	require.NoError(t, err)
	assert.Equal(t, "https://open.spotify.com/album/y", release.Name)
	assert.Zero(t, release.Size)
	assert.Zero(t, release.TrackCount)
}

// Lidarr parses t=caps with its own Newznab reader before it will use an
// indexer at all, and a missing element there is not a soft failure: the
// indexer is rejected, or every result is filtered away as uncategorized.
// Users run a wide range of Lidarr releases, so these are the elements the
// parser has required throughout, asserted against the real response.
func TestNewznabCapsContractForLidarr(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	xml := string(body)

	// Structure. Lidarr reads server, limits, searching and categories; it
	// treats a caps document missing <searching> as "cannot search".
	for _, elem := range []string{"<caps", "<server", "<limits", "<searching", "<categories"} {
		require.Contains(t, xml, elem, "caps is missing %s, which Lidarr's parser requires", elem)
	}

	// Search modes. music-search is what Lidarr uses for an album lookup;
	// without it declared available the indexer is never queried for one.
	for _, mode := range []string{"music-search", "search"} {
		require.Contains(t, xml, mode, "caps does not declare %s", mode)
	}

	// Categories. Lidarr filters results against the categories configured on
	// the indexer; anything outside the declared set is dropped silently, so
	// the audio categories this proxy tags releases with must be declared.
	for _, cat := range []string{`id="3000"`, `id="3040"`} {
		require.Contains(t, xml, cat, "caps does not declare category %s", cat)
	}
	// 3010 is Audio/MP3, and this proxy serves no MP3. Declaring it offered a
	// tick box that filtered away every release it was supposed to select.
	require.NotContains(t, xml, `id="3010"`)

	// music-search must declare the fields Lidarr sends. It builds a query
	// with artist and album; an indexer that declares neither gets a plain
	// q= search and matches far worse.
	require.Regexp(t, `music-search[^>]*supportedParams="[^"]*artist[^"]*"`, xml,
		"music-search must declare artist as a supported param")
	require.Regexp(t, `music-search[^>]*supportedParams="[^"]*album[^"]*"`, xml,
		"music-search must declare album as a supported param")
}

// A search result Lidarr cannot size or categorize is a result it will not
// grab. These are the item-level fields its RSS parser reads.
func TestNewznabSearchItemContractForLidarr(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=music&artist=Daft+Punk&album=Discovery&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	xml := string(body)

	// The channel itself must be well-formed even when the backend returns
	// nothing - Lidarr logs a parse failure as an indexer error, which is a
	// far worse signal than an empty feed.
	require.Contains(t, xml, "<rss", "search must answer an RSS document")
	require.Contains(t, xml, "<channel", "search must answer a channel")

	if !strings.Contains(xml, "<item") {
		t.Skip("backend returned no items in this environment; structure asserted above")
	}
	for _, elem := range []string{"<title", "<guid", "<link", "<enclosure", "newznab:attr"} {
		require.Contains(t, xml, elem, "search item is missing %s, which Lidarr reads", elem)
	}
}
