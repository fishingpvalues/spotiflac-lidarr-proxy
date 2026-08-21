package spotiflac_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// mockPython creates a fake python3 script that outputs JSON progress events.
// It creates a dummy output file so the "complete" event has a real path for os.Stat.
func mockPython(t *testing.T, responses []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "python3")

	content := "#!/bin/bash\n"
	if len(responses) > 0 {
		for _, r := range responses {
			content += fmt.Sprintf("echo '%s'\n", r)
		}
	}
	// Parse --output-dir and create a dummy file for the "complete" path.
	content += `OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ -n "$OUTDIR" ]]; then
  mkdir -p "$OUTDIR"
  touch "$OUTDIR/dummy.flac"
fi
exit ` + fmt.Sprintf("%d", exitCode) + "\n"

	require.NoError(t, os.WriteFile(script, []byte(content), 0755))
	return script
}

// mockCLIForCascade creates a fake spotiflac-cli that outputs given JSON lines
// and creates a dummy output file.
func mockCLIForCascade(t *testing.T, responses []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "spotiflac-cli")

	content := "#!/bin/bash\n"
	if len(responses) > 0 {
		for _, r := range responses {
			content += fmt.Sprintf("echo '%s'\n", r)
		}
	}
	content += `OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ -n "$OUTDIR" ]]; then
  mkdir -p "$OUTDIR"
  touch "$OUTDIR/dummy.flac"
fi
exit ` + fmt.Sprintf("%d", exitCode) + "\n"

	require.NoError(t, os.WriteFile(script, []byte(content), 0755))
	return script
}

// TestCollectPythonResultForwardsAfterComplete verifies collectPythonResult
// only forwards events after a "complete" event arrives, and returns true.
func TestCollectPythonResultForwardsAfterComplete(t *testing.T) {
	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)

	pyEvents := make(chan spotiflac.ProgressEvent, 32)
	pyErrs := make(chan error, 1)
	mainEvents := make(chan spotiflac.ProgressEvent, 32)
	mainErrs := make(chan error, 1)

	// Feed Python events: pre-complete events should be dropped.
	go func() {
		pyEvents <- spotiflac.ProgressEvent{Type: "status", Artist: "status-before", Title: "should-drop"}
		pyEvents <- spotiflac.ProgressEvent{Type: "progress", Percent: 50}
		pyEvents <- spotiflac.ProgressEvent{Type: "complete", OutputPath: "/tmp/test.flac", Size: 1000, Artist: "Test Artist", Album: "Test Album"}
		close(pyEvents)
		pyErrs <- nil
		close(pyErrs)
	}()

	ok := client.CollectPythonResult(pyEvents, pyErrs, mainEvents, mainErrs)
	close(mainEvents)
	close(mainErrs)
	assert.True(t, ok)

	// Drain main channels.
	var gotEvents []spotiflac.ProgressEvent
	for evt := range mainEvents {
		gotEvents = append(gotEvents, evt)
	}
	assert.Len(t, gotEvents, 1, "only the complete event should be forwarded")
	assert.Equal(t, "complete", gotEvents[0].Type)
	assert.Equal(t, "Test Artist", gotEvents[0].Artist)
}

// TestCollectPythonResultReturnsFalseOnNoComplete verifies collectPythonResult
// returns false when no "complete" event arrives, signaling the caller to try CLI.
func TestCollectPythonResultReturnsFalseOnNoComplete(t *testing.T) {
	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)

	pyEvents := make(chan spotiflac.ProgressEvent, 32)
	pyErrs := make(chan error, 1)
	mainEvents := make(chan spotiflac.ProgressEvent, 32)
	mainErrs := make(chan error, 1)

	// Python emits events but no "complete" — e.g. it errored out.
	go func() {
		pyEvents <- spotiflac.ProgressEvent{Type: "status", Artist: "trying..."}
		pyEvents <- spotiflac.ProgressEvent{Type: "error", ErrorMessage: "all services failed"}
		close(pyEvents)
		close(pyErrs)
	}()

	ok := client.CollectPythonResult(pyEvents, pyErrs, mainEvents, mainErrs)
	close(mainEvents)
	close(mainErrs)
	assert.False(t, ok)
}

// TestDownloadPythonSucceedsCLINotInvoked verifies that when Python wrapper
// succeeds (emits "complete"), the CLI fallback is never called.
func TestDownloadPythonSucceedsCLINotInvoked(t *testing.T) {
	pythonBin := mockPython(t, []string{
		`{"type":"status","message":"downloading via SpotiFLAC module"}`,
		`{"type":"progress","track":"01","percent":50}`,
		`{"type":"track_done","track":"01.flac","title":"Test Song","artist":"Test Artist","album":"Test Album","path":"/tmp/out/01.flac"}`,
		`{"type":"complete","path":"/tmp/out/01.flac","size":12345,"artist":"Test Artist","album":"Test Album","title":"Test Song"}`,
	}, 0)

	// CLI script that would fail noisily if invoked — it must never be called.
	cliPath := mockCLIForCascade(t, []string{
		`{"type":"error","message":"CLI SHOULD NOT HAVE BEEN CALLED"}`,
	}, 1)

	client := spotiflac.NewClient(cliPath, 30*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, nil)

	outputDir := t.TempDir()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		outputDir, "", "")

	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				// Both channels closed — done.
				events = nil
			} else {
				gotEvents = append(gotEvents, evt)
			}
			if events == nil && errs == nil {
				goto done
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
			} else if e != nil {
				gotErrs = append(gotErrs, e)
			}
			if events == nil && errs == nil {
				goto done
			}
		}
	}
done:

	// The CLI error about "SHOULD NOT HAVE BEEN CALLED" must not appear.
	for _, e := range gotErrs {
		assert.NotContains(t, e.Error(), "SHOULD NOT HAVE BEEN CALLED",
			"CLI must not be invoked when Python succeeds")
	}

	// Complete event must be present.
	var foundComplete bool
	for _, evt := range gotEvents {
		if evt.Type == "complete" {
			foundComplete = true
			assert.Equal(t, "Test Artist", evt.Artist)
			assert.Equal(t, int64(12345), evt.Size)
		}
	}
	assert.True(t, foundComplete, "complete event must be emitted")
}

// TestDownloadPythonFailsFallsThroughToCLI verifies the cascade: Python fails
// (no "complete"), so CLI is invoked as fallback and succeeds.
func TestDownloadPythonFailsFallsThroughToCLI(t *testing.T) {
	pythonBin := mockPython(t, []string{
		`{"type":"status","message":"trying tidal..."}`,
		`{"type":"error","message":"tidal: verification required"}`,
	}, 1)

	cliPath := mockCLIForCascade(t, []string{
		`{"type":"status","message":"CLI fallback: trying with custom API URL"}`,
		`{"type":"progress","track":"01","percent":100}`,
		`{"type":"track_done","track":"01.flac","title":"CLI Song","artist":"CLI Artist","album":"CLI Album","path":"/tmp/out/01.flac"}`,
		`{"type":"complete","path":"/tmp/out/01.flac","size":99999,"artist":"CLI Artist","album":"CLI Album","title":"CLI Song"}`,
	}, 0)

	client := spotiflac.NewClient(cliPath, 30*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, nil)

	outputDir := t.TempDir()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		outputDir, "", "")

	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				events = nil
			} else {
				gotEvents = append(gotEvents, evt)
			}
			if events == nil && errs == nil {
				goto done
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
			} else if e != nil {
				gotErrs = append(gotErrs, e)
			}
			if events == nil && errs == nil {
				goto done
			}
		}
	}
done:

	require.Empty(t, gotErrs, "CLI fallback should succeed without errors")

	var foundComplete bool
	for _, evt := range gotEvents {
		if evt.Type == "complete" {
			foundComplete = true
			assert.Equal(t, "CLI Artist", evt.Artist)
		}
	}
	assert.True(t, foundComplete, "CLI fallback must emit complete event")
}

// TestDownloadPythonNotAvailableSkipsToCLI verifies that when no Python binary
// is available, the cascade skips Python entirely and goes straight to CLI.
func TestDownloadPythonNotAvailableSkipsToCLI(t *testing.T) {
	// Pass a non-existent Python path — findPython will fall through to system
	// "python3" which should also not exist in CI/test environments.
	cliPath := mockCLIForCascade(t, []string{
		`{"type":"status","message":"CLI direct (no Python available)"}`,
		`{"type":"progress","track":"01","percent":100}`,
		`{"type":"track_done","track":"01.flac","title":"Direct CLI","artist":"Direct","album":"Album","path":"/tmp/out/01.flac"}`,
		`{"type":"complete","path":"/tmp/out/01.flac","size":55555,"artist":"Direct","album":"Album","title":"Direct CLI"}`,
	}, 0)

	client := spotiflac.NewClient(cliPath, 30*time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)

	outputDir := t.TempDir()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		outputDir, "", "")

	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				events = nil
			} else {
				gotEvents = append(gotEvents, evt)
			}
			if events == nil && errs == nil {
				goto done
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
			} else if e != nil {
				gotErrs = append(gotErrs, e)
			}
			if events == nil && errs == nil {
				goto done
			}
		}
	}
done:

	require.Empty(t, gotErrs, "CLI should succeed when Python is unavailable")

	var foundComplete bool
	for _, evt := range gotEvents {
		if evt.Type == "complete" {
			foundComplete = true
		}
	}
	assert.True(t, foundComplete, "CLI must succeed when Python is not available")
}

// TestDownloadServiceCascadeUsesConfiguredFallbacks verifies that the
// Python wrapper's --service flag includes configured fallback services
// in the correct order (primary first, then fallbacks).
func TestDownloadServiceCascadeUsesConfiguredFallbacks(t *testing.T) {
	// Python mock records the --service argument it received.
	recordFile := filepath.Join(t.TempDir(), "service-arg.txt")
	pythonBin := filepath.Join(t.TempDir(), "python3-recorder")
	script := fmt.Sprintf(`#!/bin/bash
SERVICE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo "$SERVICE" > %s
mkdir -p "$OUTDIR"
touch "$OUTDIR/dummy.flac"
echo '{"type":"complete","path":"/tmp/out/dummy.flac","size":100,"artist":"A","album":"B"}'
`, recordFile)
	require.NoError(t, os.WriteFile(pythonBin, []byte(script), 0755))

	cliPath := mockCLIForCascade(t, []string{
		`{"type":"error","message":"CLI MUST NOT BE CALLED"}`,
	}, 1)

	client := spotiflac.NewClient(cliPath, 30*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, []string{"qobuz", "deezer"})

	outputDir := t.TempDir()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		outputDir, "tidal", "")

	// Drain channels
	for range events {
	}
	for range errs {
	}

	// Read the recorded service argument.
	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	serviceArg := strings.TrimSpace(string(data))
	assert.Equal(t, "tidal,qobuz,deezer", serviceArg,
		"Python wrapper --service must be primary + configured fallbacks in order")
}

// TestDownloadServiceCascadeExcludesDuplicatePrimary ensures the primary
// service is not duplicated in the service list if it also appears in fallbacks.
func TestDownloadServiceCascadeExcludesDuplicatePrimary(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "service-arg-nodup.txt")
	pythonBin := filepath.Join(t.TempDir(), "python3-nodup")
	script := fmt.Sprintf(`#!/bin/bash
SERVICE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo "$SERVICE" > %s
mkdir -p "$OUTDIR"
touch "$OUTDIR/dummy.flac"
echo '{"type":"complete","path":"/tmp/out/dummy.flac","size":100,"artist":"A","album":"B"}'
`, recordFile)
	require.NoError(t, os.WriteFile(pythonBin, []byte(script), 0755))

	cliPath := mockCLIForCascade(t, []string{
		`{"type":"error","message":"CLI MUST NOT BE CALLED"}`,
	}, 1)

	// "tidal" appears in both the primary service AND the fallback list.
	client := spotiflac.NewClient(cliPath, 30*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, []string{"tidal", "qobuz"})

	outputDir := t.TempDir()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		outputDir, "tidal", "")

	for range events {
	}
	for range errs {
	}

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	serviceArg := strings.TrimSpace(string(data))
	assert.Equal(t, "tidal,qobuz", serviceArg,
		"primary service must not appear twice in --service list")
}

// TestPythonFailureIsLoggedNotSilentlyDropped guards the only record of why
// the backend the proxy tries first did not work. CollectPythonResult
// deliberately does not forward a Python error when there was no "complete"
// event - the caller has to fall through to the CLI rather than fail the job -
// but it used to discard the error entirely, so a backend-1 failure produced
// no log line at all and the only way to diagnose it was to run the wrapper
// by hand.
func TestPythonFailureIsLoggedNotSilentlyDropped(t *testing.T) {
	var buf bytes.Buffer
	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	client.SetLogger(zerolog.New(&buf))

	pyEvents := make(chan spotiflac.ProgressEvent)
	pyErrs := make(chan error, 1)
	mainEvents := make(chan spotiflac.ProgressEvent, 4)
	mainErrs := make(chan error, 4)

	pyErrs <- &spotiflac.DownloadError{
		Message:   "no files downloaded - all services failed",
		RawOutput: "ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download",
	}
	// Both have to be closed, exactly as downloadWithPython's defer does it:
	// the collector drains until both are done, so leaving one open hangs.
	close(pyEvents)
	close(pyErrs)

	ok := client.CollectPythonResult(pyEvents, pyErrs, mainEvents, mainErrs)
	assert.False(t, ok, "no complete event means the CLI must still be tried")

	logged := buf.String()
	assert.Contains(t, logged, "python backend failed")
	assert.Contains(t, logged, "NETWORK_ERROR: Timeout (120s) calling download",
		"the backend's own reason has to survive into the log")

	assert.Empty(t, mainErrs, "the error must not fail the job, only be recorded")
}

// TestDownloadCLIGetsFreshBudgetAfterPythonTimeout guards the production
// outage failure mode: one shared deadline for the whole cascade meant a
// Python run stuck on a dead service consumed the entire budget, and the CLI
// phase then died instantly with "start spotiflac: context deadline
// exceeded" - the fallback never ran during exactly the outages it exists
// for. The phases now get separate deadlines under the caller's context.
func TestDownloadCLIGetsFreshBudgetAfterPythonTimeout(t *testing.T) {
	// Python hangs past every budget (simulating a dead service) and is
	// killed; it must not be able to starve the CLI of its own time.
	// Pure-bash busy loop on purpose: a forked child (sleep) would inherit
	// the stdout pipe and keep it open after the parent is killed, so
	// cmd.Wait() would block until the child exits regardless of context.
	pythonBin := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(pythonBin,
		[]byte("#!/bin/bash\nend=$((SECONDS+60))\nwhile [ $SECONDS -lt $end ]; do :; done\nexit 1\n"), 0755))

	cliPath := mockCLIForCascade(t, []string{
		`{"type":"complete","path":"/tmp/out/01.flac","size":12345}`,
	}, 0)

	client := spotiflac.NewClient(cliPath, 10*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, nil)
	client.SetPythonBudget(2 * time.Second)

	outputDir := t.TempDir()
	start := time.Now()
	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test", outputDir, "", "")

	var sawComplete bool
	var gotErr error
	for sawComplete == false || errs != nil {
		select {
		case evt, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if evt.Type == "complete" {
				sawComplete = true
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			gotErr = e
		}
		if events == nil && errs == nil {
			break
		}
	}
	elapsed := time.Since(start)

	assert.True(t, sawComplete, "the CLI phase must still succeed after the Python phase burned its budget (err=%v)", gotErr)
	assert.Less(t, elapsed, 8*time.Second,
		"CLI must start within its own fresh budget, not into an expired shared context (took %s)", elapsed)
}

// TestDownloadCLISkipsPythonBackend asserts DownloadCLI never invokes the
// Python wrapper even when one is available - the handler's per-service
// fallback loop uses it precisely because the Python cascade already tried
// every service for the release.
func TestDownloadCLISkipsPythonBackend(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "python-invoked")
	pythonBin := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(pythonBin,
		[]byte("#!/bin/bash\ntouch "+marker+"\nexit 1\n"), 0755))

	cliPath := mockCLIForCascade(t, []string{
		`{"type":"complete","path":"/tmp/out/01.flac","size":12345}`,
	}, 0)

	client := spotiflac.NewClient(cliPath, 10*time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, nil)

	outputDir := t.TempDir()
	events, errs := client.DownloadCLI(context.Background(),
		"https://open.spotify.com/album/test", outputDir, "", "")

	var sawComplete bool
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				events = nil
			} else if evt.Type == "complete" {
				sawComplete = true
			}
			if events == nil && errs == nil {
				goto done
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
			if events == nil && errs == nil {
				goto done
			}
		}
	}
done:
	assert.True(t, sawComplete)
	assert.NoFileExists(t, marker, "DownloadCLI must not touch the Python backend")
}

// TestHasPythonBackend covers both halves of the check: an existing
// interpreter plus the embedded wrapper, and a missing interpreter.
func TestHasPythonBackend(t *testing.T) {
	pythonBin := mockPython(t, nil, 0)
	client := spotiflac.NewClient("echo", time.Second, "tidal", "lossless", "", "", "", nil, pythonBin, nil)
	assert.True(t, client.HasPythonBackend())

	dead := spotiflac.NewClient("echo", time.Second, "tidal", "lossless", "", "", "", nil, "/nonexistent/python3", nil)
	assert.False(t, dead.HasPythonBackend())
}
