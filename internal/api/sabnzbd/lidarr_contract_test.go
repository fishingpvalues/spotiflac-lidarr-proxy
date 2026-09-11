package sabnzbd_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// Lidarr's SABnzbd client is shared Servarr code that has accumulated fields
// over many releases, and users do not all run the same Lidarr. A field it
// reads that we stop serving does not produce a clear error: the download
// client silently stops tracking, or an import never fires.
//
// tests/apicompat checks the CURRENT Lidarr source over the network. This is
// the hermetic half: it pins the response contract itself, so a refactor
// that drops a field fails here, offline, on every run - including for the
// older Lidarr releases that read fields current main no longer mentions.
//
// jsonKind is the JSON type Lidarr's deserializer expects. Serving the right
// name with the wrong type is as broken as omitting it: Lidarr's JSON.NET
// binding throws on a string where it wants a number, which surfaces as an
// unparseable response rather than a missing field.
type fieldSpec struct {
	name string
	kind jsonKind
}

type jsonKind int

const (
	kindAny jsonKind = iota
	kindString
	kindNumber
	kindBool
	kindArray
)

func (k jsonKind) matches(v any) bool {
	switch k {
	case kindAny:
		return true
	case kindString:
		_, ok := v.(string)
		return ok
	case kindNumber:
		_, ok := v.(float64)
		return ok
	case kindBool:
		_, ok := v.(bool)
		return ok
	case kindArray:
		_, ok := v.([]any)
		return ok
	}
	return false
}

func (k jsonKind) String() string {
	return [...]string{"any", "string", "number", "bool", "array"}[k]
}

func assertFields(t *testing.T, where string, obj map[string]any, specs []fieldSpec) {
	t.Helper()
	for _, s := range specs {
		v, ok := obj[s.name]
		if !ok {
			t.Errorf("%s: missing field %q that Lidarr reads", where, s.name)
			continue
		}
		if v == nil {
			// null is acceptable for optional strings; Lidarr treats it as empty.
			continue
		}
		if !s.kind.matches(v) {
			t.Errorf("%s: field %q is %T, Lidarr expects %s", where, s.name, v, s.kind)
		}
	}
}

// getJSON issues an authenticated SABnzbd-style call and decodes the body.
func getJSON(t *testing.T, app *fiber.App, query string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("GET", "/api/sabnzbd?apikey=test-key&output=json&"+query, nil)
	require.NoError(t, err)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mode query %q", query)

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out), "mode query %q returned non-JSON", query)
	return out
}

func TestLidarrContractVersion(t *testing.T) {
	app, _ := setupTestApp(t)
	body := getJSON(t, app, "mode=version")
	assertFields(t, "mode=version", body, []fieldSpec{{"version", kindString}})
}

func TestLidarrContractGetConfig(t *testing.T) {
	app, _ := setupTestApp(t)
	body := getJSON(t, app, "mode=get_config")

	cfg, ok := body["config"].(map[string]any)
	require.True(t, ok, "mode=get_config must return a config object")
	misc, ok := cfg["misc"].(map[string]any)
	require.True(t, ok, "config.misc is where Lidarr looks for every setting below")

	// complete_dir is how Lidarr decides where finished downloads land, and
	// the retention pair is what newer Lidarr reads to decide whether the
	// client removes history behind its back.
	assertFields(t, "config.misc", misc, []fieldSpec{
		{"complete_dir", kindString},
		{"history_retention", kindString},
		{"history_retention_option", kindString},
		{"pre_check", kindBool},
	})

	// Categories must be an array of strings; Lidarr enumerates it to
	// validate the category configured on the download client.
	cats, ok := cfg["categories"].([]any)
	require.True(t, ok, "config.categories must be an array")
	require.NotEmpty(t, cats, "an empty category list makes Lidarr reject its own configured category")
}

func TestLidarrContractGetCats(t *testing.T) {
	app, _ := setupTestApp(t)
	body := getJSON(t, app, "mode=get_cats")
	cats, ok := body["categories"].([]any)
	require.True(t, ok, "mode=get_cats must return a categories array")
	require.NotEmpty(t, cats)
	for _, c := range cats {
		_, isString := c.(string)
		require.True(t, isString, "category entries must be plain strings, got %T", c)
	}
}

func TestLidarrContractQueueSlot(t *testing.T) {
	app, q := setupTestApp(t)
	require.NoError(t, q.Add(&queue.Job{
		NzoID:      "SABnzbd_nzo_contract",
		SpotifyURL: "https://open.spotify.com/album/x",
		Status:     sabtypes.StatusQueued,
		Category:   "music",
		Filename:   "Artist - Album [FLAC]",
	}))

	body := getJSON(t, app, "mode=queue")
	qb, ok := body["queue"].(map[string]any)
	require.True(t, ok, "mode=queue must return a queue object")

	assertFields(t, "queue", qb, []fieldSpec{
		{"paused", kindBool},
		{"slots", kindArray},
	})

	slots := qb["slots"].([]any)
	require.NotEmpty(t, slots, "the queued job must appear as a slot")
	slot := slots[0].(map[string]any)

	// Drop any of these and Lidarr stops tracking the download: filename is
	// the row title, mb/mbleft drive the progress bar, and an empty filename
	// in particular means Lidarr can never match the item to import it.
	assertFields(t, "queue.slots[0]", slot, []fieldSpec{
		{"nzo_id", kindString},
		{"filename", kindString},
		{"cat", kindString},
		{"status", kindString},
		{"mb", kindAny},
		{"mbleft", kindAny},
		{"timeleft", kindString},
		{"priority", kindAny},
	})
	require.NotEmpty(t, slot["filename"], "an empty filename is untrackable for Lidarr")
}

func TestLidarrContractHistorySlot(t *testing.T) {
	app, q := setupTestApp(t)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_hist",
		SpotifyURL: "https://open.spotify.com/album/y",
		Status:     sabtypes.StatusCompleted,
		Category:   "music",
		Filename:   "Artist - Album [FLAC]",
		OutputPath: t.TempDir(),
	}
	require.NoError(t, q.Add(job))
	require.NoError(t, q.MoveToHistory(job.NzoID))

	body := getJSON(t, app, "mode=history")
	hb, ok := body["history"].(map[string]any)
	require.True(t, ok, "mode=history must return a history object")
	slots, ok := hb["slots"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, slots)
	slot := slots[0].(map[string]any)

	// storage is the path Lidarr imports from; without it a completed
	// download is invisible to the importer. fail_message is what it shows
	// for a failure, and category is what routes the import.
	assertFields(t, "history.slots[0]", slot, []fieldSpec{
		{"nzo_id", kindString},
		{"name", kindString},
		{"status", kindString},
		{"storage", kindString},
		{"category", kindString},
		{"bytes", kindNumber},
	})
}

// fail_message is `omitempty`, so it is absent on a success - which is fine,
// Lidarr binds the missing key to an empty string. It must be present and
// populated on a FAILURE, because that string is the whole explanation Lidarr
// shows the user and stores in its own history.
func TestLidarrContractHistoryFailMessage(t *testing.T) {
	app, q := setupTestApp(t)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_failed",
		SpotifyURL: "https://open.spotify.com/album/f",
		Category:   "music",
		Filename:   "Artist - Album [FLAC]",
	}
	// Add() forces Queued and does not persist error_message - it is the
	// new-job path. A failure is written the way failJob writes it: update,
	// then move to history.
	require.NoError(t, q.Add(job))
	job.Status = sabtypes.StatusFailed
	job.ErrorMessage = "every service failed"
	require.NoError(t, q.Update(job))
	require.NoError(t, q.MoveToHistory(job.NzoID))

	body := getJSON(t, app, "mode=history")
	slots := body["history"].(map[string]any)["slots"].([]any)
	require.NotEmpty(t, slots)
	slot := slots[0].(map[string]any)

	msg, ok := slot["fail_message"].(string)
	require.True(t, ok, "a failed history row must carry fail_message")
	require.NotEmpty(t, msg, "fail_message must explain the failure, not be blank")
}

// Lidarr only ever sends these status strings back into its own state
// machine, so the vocabulary is part of the contract. An unknown value makes
// Lidarr treat the item as neither downloading nor done.
func TestLidarrContractStatusVocabulary(t *testing.T) {
	allowed := map[sabtypes.JobStatus]bool{
		sabtypes.StatusQueued:      true,
		sabtypes.StatusDownloading: true,
		sabtypes.StatusCompleted:   true,
		sabtypes.StatusFailed:      true,
		sabtypes.StatusPaused:      true,
	}
	for st := range allowed {
		require.NotEmpty(t, string(st), "a status constant must not be empty")
	}

	app, q := setupTestApp(t)
	for i, st := range []sabtypes.JobStatus{sabtypes.StatusQueued, sabtypes.StatusDownloading} {
		require.NoError(t, q.Add(&queue.Job{
			NzoID:      fmt.Sprintf("SABnzbd_nzo_st%d", i),
			SpotifyURL: "https://open.spotify.com/album/z",
			Status:     st,
			Category:   "music",
			Filename:   "x",
		}))
	}

	body := getJSON(t, app, "mode=queue")
	slots := body["queue"].(map[string]any)["slots"].([]any)
	for _, s := range slots {
		got := s.(map[string]any)["status"].(string)
		require.True(t, allowed[sabtypes.JobStatus(got)],
			"queue slot reported status %q, which is outside the vocabulary Lidarr understands", got)
	}
}

// The auth contract has two halves and both matter to Lidarr.
//
// mode=version is deliberately exempt: real SABnzbd answers it without a key,
// and Lidarr probes it before it has authenticated anything. Requiring a key
// there would break the Test button on a correctly configured client.
// Everything else must refuse a wrong key outright - a 200 would leave Lidarr
// believing a misconfigured client is healthy.
func TestLidarrContractAuthBoundary(t *testing.T) {
	app, _ := setupTestApp(t)

	t.Run("version is reachable without a key", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/sabnzbd?output=json&mode=version", nil)
		require.NoError(t, err)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"Lidarr probes mode=version before authenticating; it must not require a key")
	})

	for _, mode := range []string{"queue", "history", "get_config", "get_cats"} {
		t.Run("wrong key rejected on mode="+mode, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/api/sabnzbd?apikey=wrong&output=json&mode="+mode, nil)
			require.NoError(t, err)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.NotEqual(t, http.StatusOK, resp.StatusCode,
				"a wrong API key answered 200 on mode=%s", mode)
		})
	}
}
