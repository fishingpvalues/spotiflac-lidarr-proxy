package spotiflac_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// TestSearchMetadataPrefersThePythonBackend is the regression guard for the
// whole "Album match is not close enough" class of rejections. spotiflac-cli
// reports track_count 0 and year "" for every hit, and the indexer turns
// those into the release size, the `files` attr and the release year - so a
// CLI-sourced release is a 0-byte release from year 0, which Lidarr scores
// below its own album-match threshold and refuses to import.
func TestSearchMetadataPrefersThePythonBackend(t *testing.T) {
	dir := t.TempDir()
	python := writeScript(t, dir, "python3", `
echo '{"type":"search_result","entity":"album","name":"Discovery","album":"Discovery","artist":"Daft Punk","spotify_url":"https://open.spotify.com/album/x","year":2001,"track_count":14}'
`)
	cli := writeScript(t, dir, "spotiflac-cli", `
echo '{"type":"search_result","album":"","name":"Discovery","artist":"Daft Punk","spotify_url":"https://open.spotify.com/album/x","year":"","track_count":0}'
`)

	client := spotiflac.NewClient(cli, 30*time.Second, "tidal", "lossless", "", "", "", nil, python, nil)
	results, err := client.SearchMetadata(context.Background(), "daft punk discovery")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 2001, results[0].Year)
	assert.Equal(t, 14, results[0].TrackCount)
	assert.Equal(t, "album", results[0].EntityKind())
}

func TestSearchMetadataFallsBackToCLIWhenPythonFindsNothing(t *testing.T) {
	dir := t.TempDir()
	python := writeScript(t, dir, "python3", `echo '{"type":"status","message":"no results"}'`)
	cli := writeScript(t, dir, "spotiflac-cli", `
echo '{"type":"search_result","album":"","name":"Discovery","artist":"Daft Punk","spotify_url":"https://open.spotify.com/album/x","year":"2001-03-12","track_count":0}'
`)

	client := spotiflac.NewClient(cli, 30*time.Second, "tidal", "lossless", "", "", "", nil, python, nil)
	results, err := client.SearchMetadata(context.Background(), "daft punk discovery")
	require.NoError(t, err)
	require.Len(t, results, 1)
	// The CLI's own album hits carry the album name in `name` and leave
	// `album` empty; without recovering it the indexer drops the only
	// releases Lidarr can import.
	assert.Equal(t, "Discovery", results[0].Album)
	// "2001-03-12" is the other shape a year arrives in.
	assert.Equal(t, 2001, results[0].Year)
}

func TestSearchMetadataFallsBackWhenPythonIsMissing(t *testing.T) {
	dir := t.TempDir()
	cli := writeScript(t, dir, "spotiflac-cli", `
echo '{"type":"search_result","entity":"track","name":"One More Time","artist":"Daft Punk","album":"Discovery","spotify_url":"https://open.spotify.com/track/x"}'
`)

	client := spotiflac.NewClient(cli, 30*time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)
	results, err := client.SearchMetadata(context.Background(), "one more time")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "track", results[0].EntityKind())
	assert.Zero(t, results[0].Year, "an absent year stays 0 rather than becoming a bogus one")
}

func TestSearchMetadataSkipsLinesWithoutASpotifyURL(t *testing.T) {
	dir := t.TempDir()
	python := writeScript(t, dir, "python3", `
echo 'this is not json at all'
echo '{"type":"status","message":"fetching"}'
echo '{"type":"search_result","entity":"album","name":"B","artist":"A","album":"B","spotify_url":"https://open.spotify.com/album/x"}'
`)
	client := spotiflac.NewClient("/nonexistent/cli", 30*time.Second, "tidal", "lossless", "", "", "", nil, python, nil)
	results, err := client.SearchMetadata(context.Background(), "a b")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "https://open.spotify.com/album/x", results[0].SpotifyURL)
}

// TestDownloadExitErrorCarriesSubprocessOutput is the regression guard for a
// failure that reached Lidarr as nothing but "spotiflac exited: exit status
// 1". A backend that dies without emitting its own JSON "error" event still
// printed the reason - to stdout, to stderr, or both - and all of it was
// being discarded.
func TestDownloadExitErrorCarriesSubprocessOutput(t *testing.T) {
	dir := t.TempDir()
	cli := writeScript(t, dir, "spotiflac-cli", `
echo 'ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download'
echo 'pydoll: Browser failed to start within timeout' >&2
exit 1
`)

	client := spotiflac.NewClient(cli, 30*time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)
	_, errs := client.Download(context.Background(), "https://open.spotify.com/album/x", dir, "tidal", "lossless")

	var got error
	for e := range errs {
		if e != nil {
			got = e
			break
		}
	}
	require.Error(t, got)

	var de *spotiflac.DownloadError
	require.ErrorAs(t, got, &de)
	assert.Contains(t, de.RawOutput, "NETWORK_ERROR: Timeout (120s) calling download", "stdout carries the provider cascade's reasons")
	assert.Contains(t, de.RawOutput, "Browser failed to start within timeout", "stderr carries pydoll's diagnostics")
}

// TestSignificantLinesSurvivesTrailingDecoration is the regression guard for a
// capture that preserved everything except the reasons. SpotiFLAC prints a
// large ASCII summary box after each provider's outcome, so the last 4 KB of a
// failed album download held the box and "Switching to next extension" and
// none of the causes.
func TestSignificantLinesSurvivesTrailingDecoration(t *testing.T) {
	dir := t.TempDir()
	noise := strings.Repeat("=== decorative summary box line ===\n", 200)
	cli := writeScript(t, dir, "spotiflac-cli", `
echo 'ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download'
echo 'ext:amazon: Track not available: not_found_on_amazon'
`+"cat <<'EOF'\n"+noise+"EOF\nexit 1\n")

	client := spotiflac.NewClient(cli, 30*time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)
	_, errs := client.Download(context.Background(), "https://open.spotify.com/album/x", dir, "tidal", "lossless")

	var got error
	for e := range errs {
		if e != nil {
			got = e
			break
		}
	}
	require.Error(t, got)

	var de *spotiflac.DownloadError
	require.ErrorAs(t, got, &de)
	assert.Contains(t, de.RawOutput, "NETWORK_ERROR: Timeout (120s) calling download")
	assert.Contains(t, de.RawOutput, "not_found_on_amazon")
}
