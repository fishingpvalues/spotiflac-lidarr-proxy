package queue_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func newTestQueue(t *testing.T) *queue.SQLiteQueue {
	t.Helper()
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	return q
}

func TestAddAndGet(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_test001",
		SpotifyURL: "https://open.spotify.com/album/12345",
		Category:   "music-flac-16",
		Priority:   "Normal",
		Service:    "tidal",
		Quality:    "lossless",
	}
	err := q.Add(job)
	require.NoError(t, err)

	got, err := q.Get("SABnzbd_nzo_test001")
	require.NoError(t, err)

	assert.Equal(t, sabnzbd.StatusQueued, got.Status)
	assert.Equal(t, "https://open.spotify.com/album/12345", got.SpotifyURL)
	assert.NotZero(t, got.TimeAdded)
}

func TestList(t *testing.T) {
	q := newTestQueue(t)

	for i := 0; i < 3; i++ {
		job := &queue.Job{
			NzoID:      "SABnzbd_nzo_test00" + string(rune('1'+i)),
			SpotifyURL: "https://open.spotify.com/album/" + string(rune('1'+i)),
			Category:   "music-flac-16",
			Priority:   "Normal",
		}
		require.NoError(t, q.Add(job))
	}

	jobs, total, err := q.List(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, jobs, 3)
}

func TestUpdate(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:    "SABnzbd_nzo_test001",
		Filename: "Artist - Album",
	}
	require.NoError(t, q.Add(job))

	job.Status = sabnzbd.StatusDownloading
	job.Percentage = 50.0
	require.NoError(t, q.Update(job))

	got, err := q.Get("SABnzbd_nzo_test001")
	require.NoError(t, err)
	assert.Equal(t, sabnzbd.StatusDownloading, got.Status)
	assert.Equal(t, 50.0, got.Percentage)
}

func TestUpdatePersistsCLIOutput(t *testing.T) {
	q := newTestQueue(t)
	job := &queue.Job{NzoID: "SABnzbd_nzo_clioutput"}
	require.NoError(t, q.Add(job))

	job.CLIOutput = "some raw cli output for postmortem"
	require.NoError(t, q.Update(job))

	var got string
	row := q.DB().QueryRow("SELECT cli_output FROM jobs WHERE nzo_id = ?", "SABnzbd_nzo_clioutput")
	require.NoError(t, row.Scan(&got))
	assert.Equal(t, "some raw cli output for postmortem", got)
}

func TestDelete(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_test001"}
	require.NoError(t, q.Add(job))

	err := q.Delete("SABnzbd_nzo_test001", false)
	require.NoError(t, err)

	_, err = q.Get("SABnzbd_nzo_test001")
	assert.Error(t, err)
}

func TestMoveToHistory(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_test001"}
	require.NoError(t, q.Add(job))

	require.NoError(t, q.MoveToHistory("SABnzbd_nzo_test001"))

	_, err := q.Get("SABnzbd_nzo_test001")
	assert.Error(t, err)

	hjobs, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, hjobs, 1)
}

func TestFindActiveBySpotifyURL(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_dup001",
		SpotifyURL: "https://open.spotify.com/album/dup",
		Status:     sabnzbd.StatusQueued,
	}
	require.NoError(t, q.Add(job))

	found, err := q.FindActiveBySpotifyURL("https://open.spotify.com/album/dup")
	require.NoError(t, err)
	assert.Equal(t, "SABnzbd_nzo_dup001", found.NzoID)

	_, err = q.FindActiveBySpotifyURL("https://open.spotify.com/album/nonexistent")
	assert.Error(t, err)
}

func TestFindActiveBySpotifyURLIgnoresHistory(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_dup002", SpotifyURL: "https://open.spotify.com/album/dup2"}
	require.NoError(t, q.Add(job))
	require.NoError(t, q.MoveToHistory("SABnzbd_nzo_dup002"))

	_, err := q.FindActiveBySpotifyURL("https://open.spotify.com/album/dup2")
	assert.Error(t, err, "a job already moved to history should not count as a duplicate")
}

func TestRecoverStuckJobsOnStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	q1, err := queue.New(dbPath)
	require.NoError(t, err)
	job := &queue.Job{NzoID: "SABnzbd_nzo_stuck001", Status: sabnzbd.StatusQueued}
	require.NoError(t, q1.Add(job))
	job.Status = sabnzbd.StatusDownloading
	require.NoError(t, q1.Update(job))
	require.NoError(t, q1.Close())

	// Simulate restart: reopening the DB via New() must recover the stuck job.
	q2, err := queue.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { q2.Close() })

	_, err = q2.Get("SABnzbd_nzo_stuck001")
	assert.Error(t, err, "recovered job should have moved to history, not stayed in the active queue")

	hist, _, err := q2.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabnzbd.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "interrupted by restart")
}

func TestPruneHistoryKeepsOnlyMostRecent(t *testing.T) {
	q := newTestQueue(t)

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("SABnzbd_nzo_hist%d", i)
		require.NoError(t, q.Add(&queue.Job{NzoID: id}))
		require.NoError(t, q.MoveToHistory(id))
	}

	require.NoError(t, q.PruneHistory(2))

	hist, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, hist, 2)
}

// TestMigrateAddsColumnsToPreExistingDatabase guards against the single
// most important migration bug: a real deployed /data/queue.db created by a
// version of this schema that predates the track_count/cli_output columns
// must not be left behind by "CREATE TABLE IF NOT EXISTS" (a no-op against
// an already-existing table). Without an additive-column migration step,
// every subsequent Add/Update/Get call referencing those columns would fail
// at runtime with "no such column" despite queue.New succeeding.
func TestMigrateAddsColumnsToPreExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-schema.db")

	// Hand-build the OLD schema: identical to the current one but missing
	// track_count and cli_output entirely (as it would have been before
	// those columns were introduced).
	oldDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = oldDB.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			nzo_id TEXT UNIQUE NOT NULL,
			spotify_url TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'Queued',
			category TEXT NOT NULL DEFAULT 'music-flac-16',
			priority TEXT NOT NULL DEFAULT 'Normal',
			filename TEXT NOT NULL DEFAULT '',
			output_path TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			sizeleft INTEGER NOT NULL DEFAULT 0,
			percentage REAL NOT NULL DEFAULT 0.0,
			time_added DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			error_message TEXT DEFAULT '',
			service TEXT NOT NULL DEFAULT '',
			quality TEXT NOT NULL DEFAULT '',
			is_history INTEGER NOT NULL DEFAULT 0
		);
	`)
	require.NoError(t, err)

	// Seed a pre-existing row, as a real deployment would have.
	_, err = oldDB.Exec(
		`INSERT INTO jobs (nzo_id, spotify_url, service, quality) VALUES (?, ?, ?, ?)`,
		"SABnzbd_nzo_preexisting", "https://open.spotify.com/album/pre", "tidal", "lossless",
	)
	require.NoError(t, err)
	require.NoError(t, oldDB.Close())

	// Now open it through the real migration path.
	q, err := queue.New(dbPath)
	require.NoError(t, err, "New must succeed against a database missing the newer columns")
	t.Cleanup(func() { q.Close() })

	// The pre-existing row must be readable with sane zero-value defaults
	// for the newly-added columns.
	pre, err := q.Get("SABnzbd_nzo_preexisting")
	require.NoError(t, err)
	assert.Equal(t, 0, pre.TrackCount)
	assert.Equal(t, "", pre.CLIOutput)

	// A subsequent Add+Get round-trip (including the new columns) must work.
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_afterupgrade",
		SpotifyURL: "https://open.spotify.com/album/post",
		Service:    "qobuz",
		Quality:    "hires",
		TrackCount: 12,
	}
	require.NoError(t, q.Add(job))

	got, err := q.Get("SABnzbd_nzo_afterupgrade")
	require.NoError(t, err)
	assert.Equal(t, 12, got.TrackCount)

	got.CLIOutput = "postmortem output"
	require.NoError(t, q.Update(got))

	var cliOutput string
	row := q.DB().QueryRow("SELECT cli_output FROM jobs WHERE nzo_id = ?", "SABnzbd_nzo_afterupgrade")
	require.NoError(t, row.Scan(&cliOutput))
	assert.Equal(t, "postmortem output", cliOutput)
}

func TestPruneHistoryZeroMeansUnlimited(t *testing.T) {
	q := newTestQueue(t)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("SABnzbd_nzo_histunlim%d", i)
		require.NoError(t, q.Add(&queue.Job{NzoID: id}))
		require.NoError(t, q.MoveToHistory(id))
	}

	require.NoError(t, q.PruneHistory(0))

	_, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, total, "keep=0 should mean no pruning")
}
