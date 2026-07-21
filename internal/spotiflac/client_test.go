package spotiflac_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// mockCli creates a fake spotiflac-cli script that outputs JSON progress lines.
func mockCli(t *testing.T, responses []string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "spotiflac-cli")

	scriptContent := `#!/bin/bash
for line in "$@"; do echo "$line"; done
`
	if len(responses) > 0 {
		scriptContent = "#!/bin/bash\n"
		for _, r := range responses {
			scriptContent += fmt.Sprintf("echo '%s'\n", r)
		}
	}
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0755))
	return script
}

func TestDownloadProgress(t *testing.T) {
	responses := []string{
		`{"type":"progress","track":"01","title":"First Song","percent":25,"speed":"1.2MB/s"}`,
		`{"type":"progress","track":"01","title":"First Song","percent":50,"speed":"1.1MB/s"}`,
		`{"type":"progress","track":"01","title":"First Song","percent":100,"speed":"0.8MB/s"}`,
		`{"type":"metadata","artist":"Test Artist","album":"Test Album","isrc":"US-ABC-12-34567"}`,
		`{"type":"complete","path":"/tmp/Test Artist/Test Album/01 - First Song.flac","size":28765432}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 10*time.Second, "tidal", "lossless", "", "", "", nil, "")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		"/tmp/test-output",
		"", "",
	)

	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error

loop:
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				break loop
			}
			gotEvents = append(gotEvents, evt)
		case err, ok := <-errs:
			if ok {
				gotErrs = append(gotErrs, err)
			}
		}
	}

	// Drain errs if it wasn't closed yet (defensive)
	for err := range errs {
		gotErrs = append(gotErrs, err)
	}

	assert.Empty(t, gotErrs)
	assert.Len(t, gotEvents, 5)
	assert.Equal(t, "complete", gotEvents[4].Type)
	assert.Equal(t, int64(28765432), gotEvents[4].Size)
}

func TestDownloadTimeout(t *testing.T) {
	responses := []string{} // exits immediately, but timeout is 1 nanosecond
	client := spotiflac.NewClient(mockCli(t, responses), 1*time.Nanosecond, "tidal", "lossless", "", "", "", nil, "")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		"/tmp/test-output",
		"", "",
	)

	var gotErrs []error
	for err := range errs {
		gotErrs = append(gotErrs, err)
	}
	<-events // drain

	assert.NotEmpty(t, gotErrs)
}

func TestDownloadCapturesOutputOnFailure(t *testing.T) {
	responses := []string{
		`{"type":"error","message":"disk full"}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 5*time.Second, "tidal", "lossless", "", "", "", nil, "")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test", "/tmp/test-output", "", "")

	for range events {
	}
	var gotErr error
	for e := range errs {
		gotErr = e
	}
	require.Error(t, gotErr)

	var de *spotiflac.DownloadError
	require.ErrorAs(t, gotErr, &de)
	assert.Contains(t, de.RawOutput, "disk full")
}

// TestDownloadDoesNotDeadlockOnManyEvents is a regression test for a bug where
// parseProgress wrote to an intermediate, buffered channel (capacity 32) that
// was only drained after parseProgress returned. Any CLI output producing more
// than 32 cumulative events would block the write inside parseProgress forever,
// so parseProgress never returned, the outer channels were never closed, and
// Download hung indefinitely. Here we emit well over 32 progress lines before
// a final error line, and assert that Download's channels are fully drained
// and closed within a short deadline. The drain runs in a goroutine so that if
// the deadlock regresses, this test fails fast via time.After instead of
// hanging the whole test suite.
func TestDownloadDoesNotDeadlockOnManyEvents(t *testing.T) {
	const numProgressLines = 100 // well over the 32-capacity channel buffer

	responses := make([]string, 0, numProgressLines+1)
	for i := 0; i < numProgressLines; i++ {
		responses = append(responses, fmt.Sprintf(
			`{"type":"progress","track":"01","title":"Song","percent":%d,"speed":"1.0MB/s"}`, i))
	}
	responses = append(responses, `{"type":"error","message":"too many events"}`)

	client := spotiflac.NewClient(mockCli(t, responses), 5*time.Second, "tidal", "lossless", "", "", "", nil, "")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test", "/tmp/test-output", "", "")

	done := make(chan struct{})
	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error

	go func() {
		defer close(done)
		for {
			select {
			case evt, ok := <-events:
				if !ok {
					events = nil
					if errs == nil {
						return
					}
					continue
				}
				gotEvents = append(gotEvents, evt)
			case err, ok := <-errs:
				if !ok {
					errs = nil
					if events == nil {
						return
					}
					continue
				}
				gotErrs = append(gotErrs, err)
			}
		}
	}()

	select {
	case <-done:
		// good: Download's channels closed within the deadline.
	case <-time.After(10 * time.Second):
		t.Fatal("Download did not close its channels in time — deadlock regression")
	}

	assert.Len(t, gotEvents, numProgressLines)
	require.Len(t, gotErrs, 1)

	var de *spotiflac.DownloadError
	require.ErrorAs(t, gotErrs[0], &de)
	assert.Contains(t, de.RawOutput, "too many events")
}

func TestSearchMetadataArtistFallsBackToName(t *testing.T) {
	responses := []string{
		`{"type":"result","name":"Fallback Name","artist":"","album":"Some Album","spotify_url":"https://open.spotify.com/album/xyz","title":"Some Album"}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 10*time.Second, "tidal", "lossless", "", "", "", nil, "")

	results, err := client.SearchMetadata(context.Background(), "query")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Fallback Name", results[0].Artist)
}
