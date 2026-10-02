package sabnzbd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	apispotiflac "github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// failingCLI writes a mock spotiflac-cli that always fails with the given
// message, so a test can drive the handler's failure handling end to end
// without a network or a real backend.
func failingCLI(t *testing.T, message string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "spotiflac-cli")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '" + strings.ReplaceAll(message, "'", "'\\''") + "'\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func failureHandler(t *testing.T, cliPath string, fallbacks []string) (*sabnzbd.Handler, *queue.SQLiteQueue) {
	t.Helper()
	cfg := &config.Config{
		APIKey:           "test-key",
		OutputDir:        t.TempDir(),
		DefaultService:   "tidal",
		DefaultQuality:   "lossless",
		MaxConcurrent:    1,
		JobTimeout:       2 * time.Second,
		FallbackServices: fallbacks,
	}
	st := storage.New(cfg.OutputDir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	client := apispotiflac.NewClient(cliPath, 5*time.Second, "tidal", "lossless",
		"", "", "", nil, noPython, fallbacks)
	return sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test"), q
}

// The 2026-10-01 incident, in one test.
//
// An upstream "scheduled short break" arrives from the PRIMARY service, and
// the last service in the chain then fails for an unrelated, release-specific
// reason (an Amazon 404). The break gate used to look only at that LAST error,
// so nothing parked: the queue kept walking every job through the whole chain
// against a dead upstream, recorded a breaker failure per service per job,
// and 18 jobs drained into Lidarr's failed history behind "circuit open".
//
// The job must instead go back to Queued, and the gate must be armed for
// everybody else.
func TestUpstreamBreakAnnouncedByAnyServiceParksTheWholeQueue(t *testing.T) {
	cli := failingCLI(t,
		`{"message":"track X: Download failed: The server is taking a scheduled short break. Please try again in about 79 minute(s).","type":"error"}`)
	h, q := failureHandler(t, cli, []string{"qobuz", "amazon"})

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_breakany",
		SpotifyURL: "https://open.spotify.com/album/x",
		Service:    "tidal",
		Filename:   "Some Artist - Some Album",
	}
	require.NoError(t, q.Add(job))
	h.ProcessDownloadSync(job)

	back, err := q.Get(job.NzoID)
	require.NoError(t, err, "a release behind an announced upstream break must be requeued, not failed into history")
	assert.Equal(t, sabtypes.StatusQueued, back.Status)

	rem, open := h.BreakWindowForTest()
	assert.True(t, open, "the break gate must be armed from the announcement")
	assert.Greater(t, rem, time.Minute, "the announced 79 minutes must be honored, not a short default")
}

// The same chain without an announcement: a release-specific resolution
// failure is a property of the release, so it must not open a circuit that
// unrelated releases are then held behind, and it must not be retried three
// times for an answer that cannot change.
func TestPermanentFailureDoesNotOpenTheBreaker(t *testing.T) {
	cli := failingCLI(t,
		`{"message":"Download failed: songlink/songstats couldn't find Tidal URL: failed to get Tidal URL: songlink: deezer track link not found for ISRC GBUV71901432","type":"error"}`)
	h, q := failureHandler(t, cli, []string{"qobuz", "amazon"})

	for i := 0; i < 6; i++ {
		job := &queue.Job{
			NzoID:      "SABnzbd_nzo_perm" + string(rune('a'+i)),
			SpotifyURL: "https://open.spotify.com/album/x",
			Service:    "tidal",
			Filename:   "Unresolvable Release",
		}
		require.NoError(t, q.Add(job))
		h.ProcessDownloadSync(job)
	}

	for _, svc := range []string{"tidal", "qobuz", "amazon"} {
		assert.False(t, h.BreakerOpenForTest(svc),
			"an unresolvable RELEASE must never open the %s circuit - six of them would otherwise, and every other release parks behind it", svc)
	}
}

// A release-specific failure is failed, not requeued: it will never succeed,
// and an eternally pending download hides it from the operator.
func TestPermanentFailureIsReportedToLidarr(t *testing.T) {
	cli := failingCLI(t, `{"message":"track X: Download failed: Amazon API returned status 404","type":"error"}`)
	h, q := failureHandler(t, cli, []string{"qobuz"})

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_permfinal",
		SpotifyURL: "https://open.spotify.com/album/x",
		Service:    "amazon",
		Filename:   "Missing Release",
	}
	require.NoError(t, q.Add(job))
	h.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 5})
	require.NoError(t, err)
	require.Len(t, hist, 1, "the release is genuinely unavailable and has to reach Lidarr's history")
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "404")
}

// A cooldown is upstream telling the queue to stop, so it must not be
// recorded as a service failure either - those failures are what opened every
// breaker during the 2026-10-01 break window.
func TestUpstreamCooldownDoesNotOpenTheBreaker(t *testing.T) {
	cli := failingCLI(t,
		`{"message":"Download failed: the server is overloaded and taking a short break. Please try again in about 30 minute(s), thanks for your patience.","type":"error"}`)
	h, q := failureHandler(t, cli, []string{"qobuz", "amazon"})

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_cooldown",
		SpotifyURL: "https://open.spotify.com/album/x",
		Service:    "tidal",
		Filename:   "Cooling Down",
	}
	require.NoError(t, q.Add(job))
	h.ProcessDownloadSync(job)

	for _, svc := range []string{"tidal", "qobuz", "amazon"} {
		assert.False(t, h.BreakerOpenForTest(svc),
			"an announced break is not a %s outage; recording it opened all three breakers on 2026-10-01", svc)
	}
}

// mode=queue's kbpersec was `bytesDownloadedSoFar / 10` - a since-start
// average with a hardcoded divisor, so a 35 MB single reported megabytes per
// second and a stalled job kept reporting movement. It has to be a real rate,
// and it has to be 0 on the first poll because one sample cannot make a rate.
func TestQueueSpeedIsMeasuredNotFabricated(t *testing.T) {
	h, q := newProgressTestHandler(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_speed",
		SpotifyURL: "https://open.spotify.com/album/speed",
		Service:    "tidal",
		Status:     sabtypes.StatusDownloading,
		Size:       100 * 1024 * 1024,
		Sizeleft:   100 * 1024 * 1024,
		Filename:   "Speed Test",
	}
	require.NoError(t, q.Add(job))

	// First observation: no previous sample, so no rate can be reported.
	assert.Zero(t, h.ObserveSpeedForTest(job), "one sample is not a rate")

	// 50 MB in 10 s.
	job.Sizeleft = 50 * 1024 * 1024
	job.ProgressAt = ptrTime(time.Now().Add(-10 * time.Second))
	h.BackdateSpeedSampleForTest(job.NzoID, job.Sizeleft+50*1024*1024, 10*time.Second)
	got := h.ObserveSpeedForTest(job)
	assert.InDelta(t, 5*1024*1024, got, 512*1024, "the rate must reflect the observed movement, in bytes per second")

	// No movement at all reports zero, not the old since-start average.
	h.BackdateSpeedSampleForTest(job.NzoID, job.Sizeleft, 10*time.Second)
	assert.Zero(t, h.ObserveSpeedForTest(job), "a stalled download must report 0, not an average")
}

func ptrTime(t time.Time) *time.Time { return &t }
