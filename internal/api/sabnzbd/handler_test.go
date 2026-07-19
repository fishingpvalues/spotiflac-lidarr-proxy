package sabnzbd_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	apispotiflac "github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func setupTestApp(t *testing.T) (*fiber.App, *queue.SQLiteQueue) {
	t.Helper()

	cfg := &config.Config{
		APIKey:         "test-key",
		OutputDir:      t.TempDir(),
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  1,
		JobTimeout:     30 * time.Minute,
	}

	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	st := storage.New(cfg.OutputDir)

	client := apispotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "")

	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	// Immutable must match main.go's app config: fiber/fasthttp otherwise
	// hands back query/form strings that alias the connection's read buffer,
	// which addurl.go stores in a Job handed to a background goroutine that
	// outlives the request. See TestAddURLCategorySurvivesConcurrentRequest.
	app := fiber.New(fiber.Config{Immutable: true})
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "version", "auth"))
	handler.RegisterRoutes(app)

	return app, q
}

func TestVersion(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var v sabtypes.VersionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	assert.Equal(t, "0.1.0-test", v.Version)
}

func TestAuth(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=auth&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var a sabtypes.AuthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&a))
	assert.True(t, a.Auth)
}

func TestGetCats(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=get_cats&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var c sabtypes.CategoriesResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&c))
	assert.Len(t, c.Categories, 17)
}

// waitForHistory polls the queue until a job with the given nzo_id shows up
// in history (i.e. has reached a terminal state). Several tests fire an
// addurl request that kicks off a background goroutine
// (h.ProcessDownloadSync via `go`) which keeps retrying (up to 3 attempts
// with 5s/15s backoff) against the test's fake CLI before failing. Without
// waiting here, the test function returns and t.TempDir()'s automatic
// cleanup races that still-running goroutine's writes into the same
// directory, causing an intermittent "directory not empty" cleanup failure.
func waitForHistory(t *testing.T, q *queue.SQLiteQueue, nzoID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		hist, _, err := q.History(queue.ListParams{Limit: 50})
		if err != nil {
			return false
		}
		for _, j := range hist {
			if j.NzoID == nzoID {
				return true
			}
		}
		return false
	}, 60*time.Second, 100*time.Millisecond, "job %s never reached history (background goroutine still running)", nzoID)
}

func TestAddURL(t *testing.T) {
	app, q := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/test123&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var r sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	assert.True(t, r.Status)
	assert.Len(t, r.NzoIDs, 1)
	assert.Contains(t, r.NzoIDs[0], "SABnzbd_nzo_")

	// Wait for the background ProcessDownloadSync goroutine to finish (it
	// will fail against the fake "echo" CLI after retries) so it doesn't
	// race t.TempDir()'s cleanup after this test function returns.
	waitForHistory(t, q, r.NzoIDs[0])
}

// TestAddURLCategorySurvivesConcurrentRequest is a regression guard for a
// real data-corruption bug found against production this session: a job's
// stored category came back as "jsonc-flac-16" instead of the "music-flac-16"
// that was actually sent. Root cause: fiber/fasthttp's Query()/FormValue()
// return strings that alias the connection's read buffer by default, valid
// only until the handler returns. addurl.go stores that string in a Job
// handed to a `go h.ProcessDownloadSync(job)` goroutine, which re-persists
// job.Category on every later queue.Update() call as the download
// progresses - if a later, unrelated request reuses the same buffer before
// that happens, the in-memory field (and everything written from it
// afterward) silently reflects the newer request's bytes instead. Fixed by
// setting Immutable: true on the app (see setupTestApp and main.go); this
// test fires a second, differently-shaped request immediately after the
// first to exercise exactly that reuse window.
func TestAddURLCategorySurvivesConcurrentRequest(t *testing.T) {
	app, q := setupTestApp(t)

	req1, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/buffertest&cat=music-flac-16&apikey=test-key", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	var r1 sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp1.Body).Decode(&r1))
	require.Len(t, r1.NzoIDs, 1)

	// Immediately fire a differently-shaped request on the same app so any
	// buffer the first request's query string aliased is likely reused
	// before ProcessDownloadSync's later Update() calls run.
	req2, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version&apikey=test-key&filler=this-is-a-much-longer-and-differently-shaped-query-string-than-the-first-one", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	assert.Equal(t, 200, resp2.StatusCode)

	waitForHistory(t, q, r1.NzoIDs[0])

	job, err := q.Get(r1.NzoIDs[0])
	if err != nil {
		hist, _, histErr := q.History(queue.ListParams{Limit: 50})
		require.NoError(t, histErr)
		for _, j := range hist {
			if j.NzoID == r1.NzoIDs[0] {
				job = j
				break
			}
		}
	}
	require.NotNil(t, job, "job must be findable in either the active queue or history")
	assert.Equal(t, "music-flac-16", job.Category, "category must not be corrupted by a later, unrelated request reusing the same connection buffer")
}

// TestAddFileExtractsSpotifyURLFromUploadedNZB covers the mode=addfile path
// Lidarr actually uses: it fetches our synthetic NZB (see
// indexer.GenerateNZB / the newznab package's t=get) and re-uploads the
// bytes rather than passing a plain "name" URL. Regression guard for the
// grab flow discovered broken against a real production Lidarr this
// session ("Expected 'nzb' found 'html'", then a missing addfile parser).
func TestAddFileExtractsSpotifyURLFromUploadedNZB(t *testing.T) {
	app, q := setupTestApp(t)

	const spotifyURL = "https://open.spotify.com/album/addfiletest"
	nzb, err := indexer.GenerateNZB(spotifyURL, "Artist - Album", "", 1700000000)
	require.NoError(t, err)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("name", "release.nzb")
	require.NoError(t, err)
	_, err = part.Write(nzb)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addfile&apikey=test-key", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var r sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	assert.True(t, r.Status)
	require.Len(t, r.NzoIDs, 1)

	job, err := q.Get(r.NzoIDs[0])
	require.NoError(t, err)
	assert.Equal(t, spotifyURL, job.SpotifyURL, "addfile must recover the Spotify URL embedded in the uploaded NZB")

	waitForHistory(t, q, r.NzoIDs[0])
}

func TestAddURLDedupReturnsExistingNzoID(t *testing.T) {
	app, q := setupTestApp(t)

	req1, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/duptest&apikey=test-key", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	var r1 sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp1.Body).Decode(&r1))

	req2, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/duptest&apikey=test-key", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	var r2 sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&r2))

	assert.Equal(t, r1.NzoIDs[0], r2.NzoIDs[0], "re-adding the same URL should return the same nzo_id, not create a new job")

	// Wait for the background ProcessDownloadSync goroutine (started by the
	// first addurl request) to finish before the test returns, so it
	// doesn't race t.TempDir()'s cleanup.
	waitForHistory(t, q, r1.NzoIDs[0])
}

func TestAddURLRejectsInvalidSpotifyURL(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=--output-dir&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)

	var r sabtypes.StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	assert.False(t, r.Status)
}

func TestQueue(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Regression guard: Lidarr's Sabnzbd.GetQueue() does a bare foreach
	// over the deserialized slots list with no null check - "slots":null
	// (Go's zero value for a nil slice) throws a NullReferenceException
	// on every periodic poll, confirmed against a real production Lidarr
	// this session; it kept re-tripping Lidarr's own download-client
	// circuit breaker with an escalating backoff, forever.
	assert.NotContains(t, string(body), `"slots":null`, "an empty queue must marshal slots as [], not null")

	var q sabtypes.QueueResponse
	require.NoError(t, json.Unmarshal(body, &q))
	assert.Equal(t, "0.1.0-test", q.Queue.Version)
}

func TestHistory(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=history&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Same reasoning as TestQueue: Lidarr's Sabnzbd.GetHistory() also does
	// a bare foreach with no null check.
	assert.NotContains(t, string(body), `"slots":null`, "empty history must marshal slots as [], not null")

	var h sabtypes.HistoryResponse
	require.NoError(t, json.Unmarshal(body, &h))
	assert.Equal(t, "0.1.0-test", h.History.Version)
}

func TestAuthRejected(t *testing.T) {
	app, _ := setupTestApp(t)

	// version endpoint should work without API key
	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	// queue endpoint should reject without API key
	req2, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	assert.Equal(t, 401, resp2.StatusCode)
}

func TestChangeCatUpdatesServiceAndQuality(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_changecat",
		SpotifyURL: "https://open.spotify.com/album/test",
		Category:   "music-flac-16",
		Service:    "tidal",
		Quality:    "lossless",
	}
	require.NoError(t, q.Add(job))

	req, _ := http.NewRequest("GET",
		"/api/sabnzbd?mode=change_cat&value=SABnzbd_nzo_changecat&value2=music-qobuz-flac-24&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	got, err := q.Get("SABnzbd_nzo_changecat")
	require.NoError(t, err)
	assert.Equal(t, "music-qobuz-flac-24", got.Category)
	assert.Equal(t, "qobuz", got.Service)
	assert.Equal(t, "hires", got.Quality)
}

func TestProcessDownloadFailsOnTrackCountMismatch(t *testing.T) {
	outputDir := t.TempDir()
	cfg := &config.Config{
		APIKey:         "test-key",
		OutputDir:      outputDir,
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  1,
		JobTimeout:     30 * time.Second,
	}
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	st := storage.New(outputDir)

	// mockCli emits "complete" but writes only 1 file for a job expecting 2 tracks
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")

	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_mismatch",
		SpotifyURL: "https://open.spotify.com/album/test",
		Service:    "tidal",
		Quality:    "lossless",
		TrackCount: 2,
	}
	require.NoError(t, q.Add(job))

	handler.ProcessDownloadSync(job)

	got, err := q.Get("SABnzbd_nzo_mismatch")
	// job moved to history on failure, so Get (active queue only) errors
	require.Error(t, err)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "partial album")
	_ = got
}

func TestProcessDownloadRetriesBeforeFailing(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 5 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Script counts invocations via a marker file; fails first 2 times, succeeds 3rd.
	counterFile := filepath.Join(t.TempDir(), "count")
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
COUNT=0
if [[ -f "` + counterFile + `" ]]; then COUNT=$(cat "` + counterFile + `"); fi
COUNT=$((COUNT+1))
echo $COUNT > "` + counterFile + `"
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$COUNT" -lt 3 ]]; then
  echo '{"type":"error","message":"transient failure"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_retry001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/retry"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusCompleted, hist[0].Status, "job should succeed on the 3rd attempt after 2 retries")
}

func TestProcessDownloadClearsStaleFilesBetweenRetries(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 5 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Script counts invocations via a marker file. On the first invocation it
	// writes a bogus extra file into the output dir (simulating a partial
	// write from a real CLI that downloaded some tracks before the network
	// dropped) and then fails. On the second invocation it writes exactly the
	// correct file and succeeds. If the job dir isn't cleared between
	// attempts, "stale.flac" from the first attempt would still be present
	// after the retry succeeds.
	counterFile := filepath.Join(t.TempDir(), "count")
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
COUNT=0
if [[ -f "` + counterFile + `" ]]; then COUNT=$(cat "` + counterFile + `"); fi
COUNT=$((COUNT+1))
echo $COUNT > "` + counterFile + `"
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$COUNT" -lt 2 ]]; then
  touch "$OUTDIR/stale.flac"
  echo '{"type":"error","message":"transient failure"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_staleclean001",
		Service:    "tidal",
		SpotifyURL: "https://open.spotify.com/album/staleclean",
		TrackCount: 1,
	}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, sabtypes.StatusCompleted, hist[0].Status, "job should succeed on the 2nd attempt after 1 retry")

	entries, err := os.ReadDir(hist[0].OutputPath)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.NotContains(t, names, "stale.flac", "stale file from the failed first attempt must not survive into the successful retry's output dir")
	assert.Contains(t, names, "01.flac")
}

func TestProcessDownloadFallsBackToNextService(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second,
		FallbackServices: []string{"tidal", "qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Script fails for tidal, succeeds for qobuz (service passed as --service flag).
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
SERVICE=""
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$SERVICE" == "tidal" ]]; then
  echo '{"type":"error","message":"tidal unavailable"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_fallback001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/fb"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusCompleted, hist[0].Status)
	assert.Equal(t, "qobuz", hist[0].Service, "job's recorded service should reflect the fallback that actually succeeded")
}

// TestProcessDownloadAttributesFallbackFailureToPrimaryBreaker guards against
// a bug where job.Service is mutated to the fallback's name before failJob
// records a breaker failure, so the primary service's own exhausted-retries
// failure was never recorded against its own breaker (only ever the
// last-tried fallback's). It runs enough jobs (primary "tidal", fallback
// "amazon", both always failing) to trip the breaker threshold (5) purely
// from primary-attributed failures, then confirms a subsequent tidal-only job
// (no fallback configured) short-circuits on an open breaker -- which could
// only happen if "tidal"'s own failures were actually being recorded each
// time, despite job.Service having been mutated to "amazon" by the time
// failJob used to run.
func TestProcessDownloadAttributesFallbackFailureToPrimaryBreaker(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second,
		FallbackServices: []string{"tidal", "amazon"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Both tidal and amazon always fail.
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
echo '{"type":"error","message":"unavailable"}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	for i := 0; i < 5; i++ {
		job := &queue.Job{
			NzoID:      fmt.Sprintf("SABnzbd_nzo_primaryfail%d", i),
			Service:    "tidal",
			SpotifyURL: "https://open.spotify.com/album/pf",
		}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job)
	}

	// A fresh job targeting "tidal" alone (no fallback configured this time,
	// same handler/breaker) must short-circuit: proof that tidal's own
	// breaker recorded all 5 failures above, not just amazon's.
	cfg.FallbackServices = nil
	job := &queue.Job{NzoID: "SABnzbd_nzo_primaryfail_check", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/check"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 20})
	require.NoError(t, err)
	var found bool
	for _, j := range hist {
		if j.NzoID == "SABnzbd_nzo_primaryfail_check" {
			found = true
			assert.Contains(t, j.ErrorMessage, "circuit open", "primary service's breaker should have opened from its own recorded failures during the fallback runs")
		}
	}
	assert.True(t, found)
}

// TestProcessDownloadClearsStaleFilesAcrossServiceFallback guards against a
// bug where the job dir is only cleared between retries of the SAME service,
// not when transitioning from the primary's exhausted attempts to the first
// fallback attempt. A stale file written by the primary's last failed
// attempt must not survive into the successful fallback's output dir.
func TestProcessDownloadClearsStaleFilesAcrossServiceFallback(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second,
		FallbackServices: []string{"tidal", "qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// tidal always writes a stale extra file before failing (simulating a
	// partial write from a dropped connection); qobuz writes exactly the
	// correct file and succeeds. If the dir isn't cleared on the primary ->
	// fallback transition, "stale.flac" from tidal's last attempt would
	// still be present in qobuz's "successful" output dir.
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
SERVICE=""
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$SERVICE" == "tidal" ]]; then
  touch "$OUTDIR/stale.flac"
  echo '{"type":"error","message":"tidal unavailable"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_fbstaleclean001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/fbstale"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, sabtypes.StatusCompleted, hist[0].Status)
	require.Equal(t, "qobuz", hist[0].Service)

	entries, err := os.ReadDir(hist[0].OutputPath)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.NotContains(t, names, "stale.flac", "stale file from the failed primary attempt must not survive into the successful fallback's output dir")
	assert.Contains(t, names, "01.flac")
}

func TestProcessDownloadShortCircuitsWhenBreakerOpen(t *testing.T) {
	app, q := setupTestApp(t)
	_ = app

	cfg := &config.Config{OutputDir: t.TempDir(), MaxConcurrent: 1, JobTimeout: 5 * time.Second}
	st := storage.New(cfg.OutputDir)
	client := apispotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	// Force the breaker open by feeding 5 consecutive failures for "tidal".
	for i := 0; i < 5; i++ {
		job := &queue.Job{NzoID: fmt.Sprintf("SABnzbd_nzo_fail%d", i), Service: "tidal", SpotifyURL: "bad"}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job) // "echo" exits 0 with no "complete" event -> fails
	}

	job := &queue.Job{NzoID: "SABnzbd_nzo_shortcircuit", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/x"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 20})
	require.NoError(t, err)
	var found bool
	for _, j := range hist {
		if j.NzoID == "SABnzbd_nzo_shortcircuit" {
			found = true
			assert.Contains(t, j.ErrorMessage, "circuit open")
		}
	}
	assert.True(t, found)
}

// TestProcessDownloadFallsBackWhenPrimaryBreakerAlreadyOpen guards against a
// bug where an already-open primary breaker caused processDownload to fail
// the job immediately without ever consulting the configured fallback
// chain. If the fallback chain has a healthy service, the job should
// succeed via that fallback instead of failing outright.
func TestProcessDownloadFallsBackWhenPrimaryBreakerAlreadyOpen(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:     dir,
		MaxConcurrent: 1,
		JobTimeout:    5 * time.Second,
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// tidal always fails; qobuz always succeeds.
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
SERVICE=""
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$SERVICE" == "tidal" ]]; then
  echo '{"type":"error","message":"tidal unavailable"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	// Trip tidal's breaker with 5 consecutive real failures (no fallback
	// configured yet), matching how a breaker actually opens.
	for i := 0; i < 5; i++ {
		job := &queue.Job{
			NzoID:      fmt.Sprintf("SABnzbd_nzo_preopen%d", i),
			Service:    "tidal",
			SpotifyURL: "https://open.spotify.com/album/preopen",
		}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job)
	}

	// Now configure a healthy fallback and submit a fresh job against the
	// (now open) tidal breaker. The fix under test: processDownload must
	// skip the primary attempt (breaker open) and go straight to the
	// fallback chain rather than failing the job immediately.
	cfg.FallbackServices = []string{"tidal", "qobuz"}
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_breakeropenfallback",
		Service:    "tidal",
		SpotifyURL: "https://open.spotify.com/album/breakeropenfallback",
	}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 20})
	require.NoError(t, err)
	var found *queue.Job
	for _, j := range hist {
		if j.NzoID == "SABnzbd_nzo_breakeropenfallback" {
			found = j
		}
	}
	require.NotNil(t, found, "job should have reached history")
	assert.Equal(t, sabtypes.StatusCompleted, found.Status, "job should succeed via the healthy fallback instead of failing on the already-open primary breaker")
	assert.Equal(t, "qobuz", found.Service, "job's recorded service should reflect the fallback that actually succeeded")
}

func TestWarningsSurfacesOpenBreaker(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 2 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	client := apispotiflac.NewClient("false", 2*time.Second, "tidal", "lossless", "", "", "") // "false" always exits 1
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	for i := 0; i < 5; i++ {
		job := &queue.Job{NzoID: fmt.Sprintf("SABnzbd_nzo_warn%d", i), Service: "tidal", SpotifyURL: "https://open.spotify.com/album/x"}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job)
	}

	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "version", "auth"))
	handler.RegisterRoutes(app)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=warnings&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)

	var w sabtypes.WarningsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&w))
	require.NotEmpty(t, w.Warnings)
	assert.Contains(t, w.Warnings[0].Text, "tidal")
}

// TestWarningsSurfacesStuckJob guards against handleWarnings only ever
// checking open circuit breakers and never noticing a job stuck in
// Downloading well past its expected timeout -- a sign of a wedged CLI
// subprocess or a job that fell through some other gap in the retry/breaker
// pipeline.
func TestWarningsSurfacesStuckJob(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 1 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	client := apispotiflac.NewClient("echo", 1*time.Second, "tidal", "lossless", "", "", "")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_stuckwarn",
		SpotifyURL: "https://open.spotify.com/album/stuck",
		Service:    "tidal",
		Filename:   "Stuck Artist - Stuck Album",
	}
	require.NoError(t, q.Add(job))
	job.Status = sabtypes.StatusDownloading
	require.NoError(t, q.Update(job))

	// Backdate time_added directly (Update doesn't expose it) to simulate a
	// job that's been "downloading" for far longer than 2x the 1s timeout.
	_, err = q.DB().Exec(
		"UPDATE jobs SET time_added = ? WHERE nzo_id = ?",
		time.Now().Add(-1*time.Hour), job.NzoID,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "version", "auth"))
	handler.RegisterRoutes(app)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=warnings&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)

	var w sabtypes.WarningsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&w))
	require.NotEmpty(t, w.Warnings)

	var found bool
	for _, warn := range w.Warnings {
		if warn.ID == "stuck_"+job.NzoID {
			found = true
			assert.Contains(t, warn.Text, job.NzoID)
			assert.Contains(t, warn.Text, "Stuck Artist - Stuck Album")
		}
	}
	assert.True(t, found, "warnings should surface the job stuck in Downloading past 2x its timeout")
}
