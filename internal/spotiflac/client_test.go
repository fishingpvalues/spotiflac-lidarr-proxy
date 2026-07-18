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
	client := spotiflac.NewClient(mockCli(t, responses), 10*time.Second, "tidal", "lossless")

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
			if !ok {
				break loop
			}
			gotErrs = append(gotErrs, err)
		}
	}

	assert.Empty(t, gotErrs)
	assert.Len(t, gotEvents, 5)
	assert.Equal(t, "complete", gotEvents[4].Type)
	assert.Equal(t, int64(28765432), gotEvents[4].Size)
}

func TestDownloadTimeout(t *testing.T) {
	responses := []string{} // exits immediately, but timeout is 1 nanosecond
	client := spotiflac.NewClient(mockCli(t, responses), 1*time.Nanosecond, "tidal", "lossless")

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
