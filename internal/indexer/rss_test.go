package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// Lidarr's RSS sync issues t=music with no q, no artist and no album. The
// resulting empty query used to be shelled out to spotiflac-cli, which
// rejects --search "" with exit status 1, so every RSS refresh logged
// "newznab music search failed". Nothing is browsable here, so answer with
// no releases without spawning anything.
func TestSearchWithEmptyQueryDoesNotInvokeCLI(t *testing.T) {
	// A CLI path that cannot exist: if Search shells out at all, it errors.
	client := spotiflac.NewClient("/nonexistent/spotiflac-cli", time.Minute,
		"tidal", "lossless", "", "", "", nil, "", nil)

	for _, tc := range []struct{ query, artist, album string }{
		{"", "", ""},
		{"   ", "", ""},
	} {
		results, err := Search(context.Background(), client, tc.query, tc.artist, tc.album)
		if err != nil {
			t.Errorf("Search(%q,%q,%q) error = %v, want nil", tc.query, tc.artist, tc.album, err)
		}
		if len(results) != 0 {
			t.Errorf("Search(%q,%q,%q) returned %d results, want 0", tc.query, tc.artist, tc.album, len(results))
		}
	}
}
