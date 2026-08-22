package sabnzbd_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// TestHistoryDeleteRemovesEntry covers mode=history&name=delete - Lidarr's
// RemoveFromHistory call. Before the fix this silently returned the history
// list instead of deleting, so every RemoveFromHistory was a no-op.
func TestHistoryDeleteRemovesEntry(t *testing.T) {
	app, q := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/histdel&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	var r sabtypes.AddURLResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	require.Len(t, r.NzoIDs, 1)
	nzoID := r.NzoIDs[0]

	waitForHistory(t, q, nzoID)

	delReq, _ := http.NewRequest("GET", "/api/sabnzbd?mode=history&name=delete&value="+nzoID+"&del_files=1&archive=1&apikey=test-key", nil)
	delResp, err := app.Test(delReq)
	require.NoError(t, err)
	assert.Equal(t, 200, delResp.StatusCode)
	var dr sabtypes.StatusResponse
	require.NoError(t, json.NewDecoder(delResp.Body).Decode(&dr))
	assert.True(t, dr.Status, "history delete must report success")

	hist, _, err := q.History(queue.ListParams{Limit: 50})
	require.NoError(t, err)
	for _, j := range hist {
		assert.NotEqual(t, nzoID, j.NzoID, "deleted entry must be gone from history")
	}
}

// TestQueueFilterAcceptsCategoryParam covers the parameter-name mismatch with
// Lidarr's Sabnzbd client: it polls the queue with ?category=music while real
// SABnzbd callers use ?cat=. Both must filter identically. Jobs are inserted
// directly (no HTTP dispatch) so they deterministically stay in the active
// queue instead of racing the echo client into history.
func TestQueueFilterAcceptsCategoryParam(t *testing.T) {
	app, q := setupTestApp(t)

	musicNzo := "SABnzbd_nzo_catfiltera"
	flacNzo := "SABnzbd_nzo_catfilterb"
	for _, j := range []*queue.Job{
		{NzoID: musicNzo, SpotifyURL: "https://open.spotify.com/album/catfiltera", Status: sabtypes.StatusQueued, Category: "music", Filename: "A"},
		{NzoID: flacNzo, SpotifyURL: "https://open.spotify.com/album/catfilterb", Status: sabtypes.StatusQueued, Category: "music-flac-16", Filename: "B"},
	} {
		require.NoError(t, q.Add(j))
	}

	for _, param := range []string{"cat", "category"} {
		req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue&"+param+"=music&apikey=test-key", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		var qr sabtypes.QueueResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&qr))
		var ids []string
		for _, s := range qr.Queue.Slots {
			ids = append(ids, s.NzoID)
		}
		assert.Contains(t, ids, musicNzo, "param %q: music job must be listed", param)
		assert.NotContains(t, ids, flacNzo, "param %q: other-category job must be filtered out", param)
	}
}

// TestChangeCatAcceptsCatParam covers change_cat with real SABnzbd's
// parameter name (?cat=<new>) alongside the legacy ?value2=. The job is
// inserted directly so it stays in the active queue for the duration.
func TestChangeCatAcceptsCatParam(t *testing.T) {
	app, q := setupTestApp(t)

	nzoID := "SABnzbd_nzo_changecat"
	require.NoError(t, q.Add(&queue.Job{
		NzoID: nzoID, SpotifyURL: "https://open.spotify.com/album/changecat",
		Status: sabtypes.StatusQueued, Category: "music", Filename: "C",
	}))

	chgReq, _ := http.NewRequest("GET", "/api/sabnzbd?mode=change_cat&value="+nzoID+"&cat=music-tidal&apikey=test-key", nil)
	chgResp, err := app.Test(chgReq)
	require.NoError(t, err)
	assert.Equal(t, 200, chgResp.StatusCode)
	var cr sabtypes.StatusResponse
	require.NoError(t, json.NewDecoder(chgResp.Body).Decode(&cr))
	assert.True(t, cr.Status, "change_cat with ?cat= must succeed")

	job, err := q.Get(nzoID)
	require.NoError(t, err)
	assert.Equal(t, "music-tidal", job.Category)
}

// TestAddFileFormCategory covers the form-encoded variant of addfile: a
// caller that posts cat as a multipart form field (real SABnzbd accepts both
// placements) must not silently fall back to the default category.
func TestAddFileFormCategory(t *testing.T) {
	app, q := setupTestApp(t)

	const spotifyURL = "https://open.spotify.com/album/formcat"
	nzb, err := indexer.GenerateNZB(spotifyURL, "Artist - FormCat", "", time.Now().Unix())
	require.NoError(t, err)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("name", "release.nzb")
	require.NoError(t, err)
	_, err = part.Write(nzb)
	require.NoError(t, err)
	field, err := writer.CreateFormField("cat")
	require.NoError(t, err)
	_, err = field.Write([]byte("music-qobuz"))
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
	assert.Equal(t, "music-qobuz", job.Category, "form-field cat must be honored, not replaced by the default")

	// Let the background download finish before t.TempDir() cleanup races
	// its writes (same reason as the other addfile tests).
	waitForHistory(t, q, r.NzoIDs[0])
}
