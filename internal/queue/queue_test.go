package queue_test

import (
	"testing"

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
