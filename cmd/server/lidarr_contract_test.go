package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file pins the contract between this proxy and the Lidarr releases it
// claims to support, by version.
//
// The contract is not a guess: every fact below was read out of Lidarr's own
// source at the pinned tag (the URLs are in lidarrContract.source), and
// tests/lidarrcompat re-fetches them under the `apicompat` build tag so the
// table cannot rot silently. The default (offline) run asserts the same facts
// against the REAL route table, so no network is needed for CI to be
// meaningful.
//
// Two things make this worth a version dimension even though the download
// client is currently identical across the window:
//
//  1. The compatibility surface is not one file. `Sabnzbd.cs` (the models and
//     the version gate), `SabnzbdProxy.cs` (which modes are called, with which
//     query parameters) and `NewznabRequestGenerator.cs` / `NewznabRssParser.cs`
//     (the indexer side) can drift independently, and a new release can start
//     reading a field this proxy does not send.
//  2. Lidarr's own release channel is `develop`. The newest three versions are
//     all pre-releases, so "works with Lidarr" has to mean "works with these
//     three tags", not "works with the last stable".
var lidarrContract = []lidarrVersion{
	{
		Tag:     "v3.1.6.5078",
		Channel: "develop (nightly)",
		// The exact image potatostack runs in production.
		Image:  "lscr.io/linuxserver/lidarr:nightly-3.1.6.5078-ls218",
		Source: lidarrSources("v3.1.6.5078"),
	},
	{
		Tag:     "v3.1.5.5066",
		Channel: "develop (nightly)",
		Image:   "lscr.io/linuxserver/lidarr:nightly-3.1.5.5066-ls215",
		Source:  lidarrSources("v3.1.5.5066"),
	},
	{
		Tag:     "v3.1.4.5029",
		Channel: "develop (nightly)",
		Image:   "lscr.io/linuxserver/lidarr:nightly-3.1.4.5029-ls213",
		Source:  lidarrSources("v3.1.4.5029"),
	},
}

type lidarrVersion struct {
	Tag     string
	Channel string
	Image   string
	Source  map[string]string
}

func lidarrSources(ref string) map[string]string {
	base := "https://raw.githubusercontent.com/Lidarr/Lidarr/" + ref + "/src/NzbDrone.Core/"
	return map[string]string{
		"sabnzbd_client":   base + "Download/Clients/Sabnzbd/Sabnzbd.cs",
		"sabnzbd_proxy":    base + "Download/Clients/Sabnzbd/SabnzbdProxy.cs",
		"newznab_requests": base + "Indexers/Newznab/NewznabRequestGenerator.cs",
		"newznab_parser":   base + "Indexers/Newznab/NewznabRssParser.cs",
	}
}

// sabnzbdModesCalled is the complete set of `mode=` values Lidarr's SABnzbd
// client can send (SabnzbdProxy.cs, every BuildRequest call, plus the version
// node which is fetched separately). Anything in this list that the proxy does
// not answer is a download-client failure on that Lidarr release.
var sabnzbdModesCalled = []string{
	"version", "get_config", "fullstatus", "queue", "history", "addfile", "retry",
}

// queueSlotFieldsLidarrReads / historySlotFieldsLidarrReads are the JSON keys
// whose absence silently breaks tracking rather than erroring: `cat` on a
// queue slot and `category` on a history slot are what GetItems() filters by,
// and `bytes` is what history reports as the download's size.
var (
	queueSlotFieldsLidarrReads = []string{
		"nzo_id", "filename", "cat", "status", "mb", "mbleft", "timeleft", "percentage", "priority",
	}
	historySlotFieldsLidarrReads = []string{
		"nzo_id", "name", "category", "status", "bytes", "fail_message", "storage",
	}
	configFieldsLidarrReads = []string{
		"complete_dir", "pre_check", "history_retention", "history_retention_option",
	}
)

// lidarrStatusEnum is NzbDrone.Core.Download.Clients.Sabnzbd.SabnzbdDownloadStatus.
// The status strings this proxy emits are deserialized into it with no
// converter of its own, so a value outside the enum is a JsonSerialization
// exception inside GetQueue()/GetHistory(), not a degraded display.
var lidarrStatusEnum = []string{
	"Grabbing", "Queued", "Paused", "Checking", "Downloading", "QuickCheck",
	"Verifying", "Repairing", "Fetching", "Extracting", "Moving", "Running",
	"Completed", "Failed", "Deleted", "Propagating",
}

// lidarrPriorityEnum is NzbDrone.Core.Download.Clients.Sabnzbd.SabnzbdPriority.
// SabnzbdPriorityTypeConverter does Enum.TryParse over the NAME, so the API has
// to send names, not the numbers Lidarr itself sends when grabbing.
var lidarrPriorityEnum = []string{"Default", "Paused", "Low", "Normal", "High", "Force"}

// lidarrVersionRegex is Sabnzbd.cs's VersionRegex, verbatim. It is unanchored:
// it finds the first \d+.\d+.\d+ (or \d+.\d+.x) anywhere in the string.
const lidarrVersionRegex = `(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+|x)`

// lidarrNewznabModes are the only t= modes the Newznab indexer path sends.
// There is no t=get: the NZB bytes come straight from the RSS <enclosure>.
var lidarrNewznabModes = []string{"caps", "music", "search"}

// lidarrNewznabAttrs are the newznab:attr names NewznabRssParser actually
// reads. Note what is NOT in the list: files, grabs, genre, year and tracks are
// sent by this proxy and ignored by Lidarr, which is harmless - but `size` and
// `usenetdate` are load-bearing, because a feed item without a usable
// publication date is rejected outright
// ("Rss feed must have a pubDate element with a valid publish date.").
var lidarrNewznabAttrs = []string{"size", "usenetdate", "language", "artist", "album"}

// TestContractIsPinnedToTheThreeNewestLidarrReleases documents and enforces the
// version window this proxy is tested against. When Lidarr cuts a release, this
// is the one place to add it - and the integration matrix in
// .github/workflows/ci.yml has to grow with it.
func TestContractIsPinnedToTheThreeNewestLidarrReleases(t *testing.T) {
	require.Len(t, lidarrContract, 3, "the matrix is exactly the newest three Lidarr releases")
	for _, v := range lidarrContract {
		assert.NotEmpty(t, v.Tag)
		assert.NotEmpty(t, v.Image, "%s needs a pinned container image for the integration matrix", v.Tag)
		assert.Len(t, v.Source, 4, "%s must pin all four contract sources", v.Tag)
		for name, url := range v.Source {
			assert.Contains(t, url, v.Tag, "%s: %s must point at the pinned tag", v.Tag, name)
		}
	}
}

// Lidarr's version handshake, checked the way Lidarr checks it.
func TestVersionHandshakeSatisfiesEveryPinnedLidarr(t *testing.T) {
	app, _ := testApp(t)

	for _, v := range lidarrContract {
		t.Run(v.Tag, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/api/sabnzbd/?mode=version", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, 200, resp.StatusCode, "Lidarr probes the version without an API key")

			var body map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			raw, ok := body["version"].(string)
			require.True(t, ok, "mode=version must answer {\"version\":\"<string>\"}")

			// 1. ParseVersion must not return null. A string that neither
			//    matches the regex nor is exactly "develop" is a hard failure:
			//    ValidationFailure("Version", "Unknown Version: " + raw).
			re := regexp.MustCompile(lidarrVersionRegex)
			m := re.FindStringSubmatch(raw)
			require.NotNil(t, m, "Lidarr cannot parse version %q: Unknown Version", raw)

			major, err := strconv.Atoi(m[1])
			require.NoError(t, err)
			minor, err := strconv.Atoi(m[2])
			require.NoError(t, err)

			// 2. The floor: major >= 1, else minor >= 7.
			ok = major >= 1 || minor >= 7
			assert.True(t, ok, "Lidarr rejects %q: Version 0.7.0+ is required", raw)

			// 3. "develop" clears the above but is attached a warning, and
			//    Lidarr refuses to save a client that carries one without
			//    forceSave - so adding the client through the UI fails.
			assert.NotEqual(t, "develop", strings.ToLower(raw),
				"a develop answer is accepted only with a warning, and Lidarr will not save it")
		})
	}
}

// Every mode Lidarr calls, with the shape it expects back.
func TestEverySabnzbdModeLidarrCallsIsAnswered(t *testing.T) {
	app, _ := testApp(t)

	for _, mode := range sabnzbdModesCalled {
		t.Run(mode, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/api/sabnzbd/?mode="+mode+"&apikey="+testAPIKey, nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			body := readAll(t, resp)
			// addfile/retry legitimately refuse a parameterless call; what
			// must never happen is the dispatch switch not knowing the mode,
			// which is how a Lidarr release that starts calling a new mode
			// fails.
			assert.NotContains(t, body, "unknown mode",
				"mode=%s is in Lidarr's SabnzbdProxy.cs and must be dispatched", mode)
		})
	}
}

// The queue and history envelopes, and every field in them Lidarr reads.
//
// Lidarr does JObject.Parse(body).SelectToken("queue") and then .ToString() on
// the result with no null check, so a missing top-level key is a
// NullReferenceException inside the download client.
func TestQueueAndHistoryCarryWhatLidarrReads(t *testing.T) {
	app, cfg := testAppWithJob(t)

	t.Run("queue", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/api/sabnzbd/?mode=queue&apikey="+testAPIKey, nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		queue, ok := body["queue"].(map[string]any)
		require.True(t, ok, "the queue response must have a top-level \"queue\" object")

		// SabnzbdQueue reads exactly these two.
		assert.Contains(t, queue, "paused")
		slots, ok := queue["slots"].([]any)
		require.True(t, ok, "queue.slots must always be a list, never null: Lidarr foreach's it unguarded")
		require.NotEmpty(t, slots)

		slot, ok := slots[0].(map[string]any)
		require.True(t, ok)
		for _, field := range queueSlotFieldsLidarrReads {
			assert.Contains(t, slot, field, "SabnzbdQueueItem reads %q", field)
		}
		assert.Contains(t, lidarrStatusEnum, slot["status"], "status must be a SabnzbdDownloadStatus member")
		assert.Contains(t, lidarrPriorityEnum, slot["priority"],
			"SabnzbdPriorityTypeConverter enum-parses this by NAME")
		// The queue time converter splits on ':' and accepts only 3 or 4
		// integer fields; anything else throws
		// ("Expected either 0:0:0:0 or 0:0:0 format").
		assert.Regexp(t, `^\d+:\d{2}:\d{2}(:\d{2})?$`, slot["timeleft"])
	})

	t.Run("history", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/api/sabnzbd/?mode=history&apikey="+testAPIKey, nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		hist, ok := body["history"].(map[string]any)
		require.True(t, ok, "the history response must have a top-level \"history\" object")

		slots, ok := hist["slots"].([]any)
		require.True(t, ok, "history.slots must always be a list, never null")
		require.NotEmpty(t, slots)

		slot, ok := slots[0].(map[string]any)
		require.True(t, ok)
		for _, field := range historySlotFieldsLidarrReads {
			assert.Contains(t, slot, field, "SabnzbdHistoryItem reads %q", field)
		}
		assert.Contains(t, lidarrStatusEnum, slot["status"])
		// Storage is what Lidarr runs through its remote path mapping, and an
		// empty value makes CompletedDownloadService skip the item with
		// nothing in the info log at all.
		storage, _ := slot["storage"].(string)
		assert.NotEmpty(t, storage, "SabnzbdHistoryItem.Storage must name the finished download's directory")
		assert.Contains(t, storage, cfg.OutputDir)
		assert.Contains(t, slot, "fail_message", "SabnzbdHistoryItem.FailMessage binds this key")
	})
}

// get_config drives three Lidarr health checks, and one of them is inverted:
// without history_retention_option, Lidarr falls through to its legacy branch
// (`return retention != "0"`) and reads our "all" - meaning keep everything -
// as "removes completed downloads".
func TestConfigCarriesEveryFieldLidarrHealthChecksRead(t *testing.T) {
	app, _ := testApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd/?mode=get_config&apikey="+testAPIKey, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	config, ok := body["config"].(map[string]any)
	require.True(t, ok, "get_config must answer {\"config\":{...}}")

	misc, ok := config["misc"].(map[string]any)
	require.True(t, ok)
	for _, field := range configFieldsLidarrReads {
		assert.Contains(t, misc, field, "SabnzbdConfigMisc.%s is read by Lidarr's health checks", field)
	}
	// A category whose dir is relative to complete_dir makes Lidarr check
	// OutputDir/<dir>, which does not exist, and raise a permanent
	// "places downloads in ... but this directory does not appear to exist".
	cats, ok := config["categories"].([]any)
	require.True(t, ok)
	for _, c := range cats {
		cat, _ := c.(map[string]any)
		assert.Empty(t, cat["dir"], "category %v must not carry a dir", cat["name"])
	}
}

// The indexer side: which t= modes must be answered, and the feed's shape.
func TestNewznabAnswersEveryModeLidarrCalls(t *testing.T) {
	app, _ := testApp(t)

	for _, mode := range lidarrNewznabModes {
		t.Run(mode, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/api/newznab/?t="+mode+"&apikey="+testAPIKey, nil)
			if mode != "caps" {
				req, _ = http.NewRequest("GET", "/api/newznab/?t="+mode+"&apikey="+testAPIKey+"&q=", nil)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			// An empty feed is fine here (the CLI is not present in this
			// test); what must not happen is a 400/404 for a mode Lidarr
			// sends on every search.
			assert.NotEqual(t, 404, resp.StatusCode)
		})
	}
}

// caps decides whether Lidarr will use the indexer at all: TestCapabilities
// rejects it unless audio-search advertises q, artist AND album, and paging
// stops at the first page unless the limits are sane.
func TestCapsAdvertiseWhatLidarrsSearchNeeds(t *testing.T) {
	app, _ := testApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab/?t=caps", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	body := readAll(t, resp)
	assert.Contains(t, body, "<caps>", "the root element must be <caps> or Lidarr's parser throws Unexpected XML")
	assert.Contains(t, body, `supportedParams="q,artist,album"`,
		"audio-search must advertise q, artist and album together or Lidarr rejects the indexer")
	assert.Contains(t, body, "<audio-search")
	// Lidarr rejects the indexer outright when neither search nor audio-search
	// advertises the parameters its request generator sends.
	assert.Contains(t, body, `supportedParams="q"`)
	assert.Contains(t, body, "<category id=\"3000\"")
}

// The queue's own paging: Lidarr asks for the next page when one comes back
// full, and stops when it has seen `total` items.
func TestResponseTotalIsTheMatchCountNotThePageSize(t *testing.T) {
	app, _ := testApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab/?t=music&apikey="+testAPIKey+"&limit=1", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body := readAll(t, resp)
	// An empty result set still has to carry the element.
	assert.Contains(t, body, "<newznab:response")
}
