//go:build apicompat

package apicompat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
)

// This file makes the version matrix in cmd/server/lidarr_contract_test.go
// auditable against the thing it describes.
//
// That test asserts the contract offline, from a table. A table can rot: a
// Lidarr release can start calling a new mode or reading a new field, and the
// offline test keeps passing because the table still says what it said
// yesterday. This file re-derives the table from Lidarr's own source at each
// pinned tag - so the table is checked against upstream, and a drift shows up
// as a failing job instead of as a download client that quietly stops
// importing.
//
// Run with: go test -tags apicompat ./tests/apicompat/
const lidarrLidarrRepo = "Lidarr/Lidarr"

// lidarrPinnedTags is the support window. It must match the table in
// cmd/server/lidarr_contract_test.go; TestPinnedTagsAreTheNewestReleases
// checks that it is also what GitHub currently calls newest.
var lidarrPinnedTags = []string{"v3.1.6.5078", "v3.1.5.5066", "v3.1.4.5029"}

func rawURL(tag, path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/src/NzbDrone.Core/%s", lidarrLidarrRepo, tag, path)
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("cannot fetch %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("cannot fetch %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

// TestPinnedTagsAreTheNewestReleases fails when Lidarr has shipped something
// newer than the oldest entry in the window, which is the signal that the
// matrix needs a new row rather than a code change.
func TestPinnedTagsAreTheNewestReleases(t *testing.T) {
	resp, err := http.Get("https://api.github.com/repos/" + lidarrLidarrRepo + "/releases?per_page=30")
	if err != nil {
		t.Skipf("GitHub API unreachable: %v", err)
	}
	defer resp.Body.Close()

	var releases []struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		t.Skipf("cannot decode releases: %v", err)
	}

	var tags []string
	for _, r := range releases {
		if r.Draft {
			continue
		}
		tags = append(tags, r.TagName)
	}
	require3(t, len(tags) >= 3, "need at least three releases to compare, got %d", len(tags))

	newest := tags[:3]
	sort.Strings(newest)
	pinned := append([]string(nil), lidarrPinnedTags...)
	sort.Strings(pinned)
	if strings.Join(newest, ",") != strings.Join(pinned, ",") {
		t.Errorf("the support window is %v but GitHub's newest three are %v - add the new release to the matrix (cmd/server/lidarr_contract_test.go and .github/workflows/ci.yml) and drop the oldest",
			lidarrPinnedTags, tags[:3])
	}
}

// TestEveryModeLidarrCallsIsDispatched re-derives the mode list from
// SabnzbdProxy.cs at each pinned tag and checks our dispatch switch.
func TestEveryModeLidarrCallsIsDispatched(t *testing.T) {
	ourModes := extractOurModes()
	for _, tag := range lidarrPinnedTags {
		t.Run(tag, func(t *testing.T) {
			src := fetch(t, rawURL(tag, "Download/Clients/Sabnzbd/SabnzbdProxy.cs"))
			re := regexp.MustCompile(`BuildRequest\("(\w+)"`)
			seen := map[string]bool{"version": true} // fetched via node, not BuildRequest
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				seen[m[1]] = true
			}
			require3(t, len(seen) >= 5, "%s: only found %d modes, the extractor is stale", tag, len(seen))
			for mode := range seen {
				assertContains(t, ourModes, mode, "%s calls mode=%s, which this proxy does not dispatch", tag, mode)
			}
		})
	}
}

// TestEverySlotFieldLidarrReadsIsSent re-derives the fields Lidarr binds from
// its model classes at each pinned tag.
//
// The two that silently destroy the import leg are `cat` on a queue slot and
// `category` on a history slot; both were wrong in production before.
func TestEverySlotFieldLidarrReadsIsSent(t *testing.T) {
	ourTypes := extractOurTypes()
	watched := []string{
		"nzo_id", "filename", "cat", "category", "status", "mb", "mbleft",
		"timeleft", "percentage", "priority", "name", "bytes", "fail_message",
		"storage", "paused", "slots", "completedir", "complete_dir",
		"history_retention", "history_retention_option", "pre_check",
	}
	for _, tag := range lidarrPinnedTags {
		t.Run(tag, func(t *testing.T) {
			for _, class := range []string{
				"Download/Clients/Sabnzbd/Sabnzbd.cs",
				"Download/Clients/Sabnzbd/SabnzbdQueueItem.cs",
				"Download/Clients/Sabnzbd/SabnzbdHistoryItem.cs",
				"Download/Clients/Sabnzbd/SabnzbdQueue.cs",
				"Download/Clients/Sabnzbd/SabnzbdHistory.cs",
				"Download/Clients/Sabnzbd/Responses/SabnzbdConfigResponse.cs",
			} {
				src := fetch(t, rawURL(tag, class))
				for _, field := range watched {
					if !mentionsField(src, field) {
						continue
					}
					assertContains(t, ourTypes, field,
						"%s: %s binds JSON %q, which this proxy's response types do not carry",
						tag, filepath.Base(class), field)
				}
			}
		})
	}
}

// mentionsField reports whether a C# source binds the given JSON property,
// either through an explicit [JsonProperty("x")] or as a case-insensitive
// property name (the contract resolver lower-cases it).
func mentionsField(src, field string) bool {
	if strings.Contains(src, `"`+field+`"`) {
		return true
	}
	// `public string FailMessage { get; set; }` -> fail_message
	var b strings.Builder
	for i, r := range field {
		if r == '_' {
			continue
		}
		if i > 0 && field[i-1] == '_' {
			b.WriteString(strings.ToUpper(string(r)))
			continue
		}
		b.WriteRune(r)
	}
	name := b.String()
	// Capitalise the first letter, then look for it as a property.
	return regexp.MustCompile(`\b[A-Z]` + regexp.QuoteMeta(name[1:]) + `\b`).MatchString(src)
}

// TestVersionAnswerClearsEveryPinnedLidarrsGate evaluates the actual answer
// this proxy gives against each tag's own ParseVersion rules.
func TestVersionAnswerClearsEveryPinnedLidarrsGate(t *testing.T) {
	answer := extractOurVersionAnswer(t)
	for _, tag := range lidarrPinnedTags {
		t.Run(tag, func(t *testing.T) {
			src := fetch(t, rawURL(tag, "Download/Clients/Sabnzbd/Sabnzbd.cs"))

			reLine := regexp.MustCompile(`VersionRegex = new Regex\(@"([^"]+)"`).FindStringSubmatch(src)
			require3(t, reLine != nil, "%s: VersionRegex not found - the parser changed shape", tag)
			re, err := regexp.Compile(reLine[1])
			require3(t, err == nil, "%s: cannot compile %q: %v", tag, reLine[1], err)

			m := re.FindStringSubmatch(answer)
			require3(t, m != nil, "%s: cannot parse our version answer %q: Lidarr would fail with \"Unknown Version\"", tag, answer)

			major, _ := strconv.Atoi(m[1])
			minor, _ := strconv.Atoi(m[2])
			require3(t, major >= 1 || minor >= 7,
				"%s: version %q is below the 0.7.0 floor Lidarr enforces", tag, answer)

			if strings.Contains(answer, "develop") {
				t.Errorf("%s: answering %q makes Lidarr attach a warning, and it refuses to save a client that carries one", tag, answer)
			}

			// The floor check itself must still be in the file, or this test
			// would pass against a Lidarr that dropped it.
			require3(t, strings.Contains(src, `"Version 0.7.0+ is required, but found: "`),
				"%s: the minimum-version check is gone from Sabnzbd.cs - re-read the gate", tag)
		})
	}
}

// TestNewznabModesAreAnswered re-derives the t= modes from the request
// generator at each pinned tag.
func TestNewznabModesAreAnswered(t *testing.T) {
	ourModes := extractOurNewznabModes(t)
	for _, tag := range lidarrPinnedTags {
		t.Run(tag, func(t *testing.T) {
			// The generator interpolates `?t={searchType}`; the literal
			// values come from its two call sites and the capabilities
			// provider builds t=caps itself.
			src := fetch(t, rawURL(tag, "Indexers/Newznab/NewznabRequestGenerator.cs"))
			seen := map[string]bool{"caps": true}
			for _, m := range regexp.MustCompile(`"(music|search|tvsearch|movie|book)"`).FindAllStringSubmatch(src, -1) {
				seen[m[1]] = true
			}
			require3(t, seen["music"] && seen["search"],
				"%s: found %v, the extractor is stale - the generator no longer names its search types", tag, seen)
			for mode := range seen {
				assertContains(t, ourModes, mode, "%s sends t=%s, which this proxy does not handle", tag, mode)
			}
		})
	}
}

// TestNewznabCapsAdvertiseLidarrsRequiredParams checks the caps document this
// proxy serves against what each tag's parser needs.
//
// Lidarr's NewznabCapabilitiesProvider only falls back to its built-in ["q"]
// when the whole <searching> element is missing. A <search> element with no
// supportedParams parses to an empty list, SupportsSearch() answers false, and
// the t=search fallback tier is silently unusable.
func TestNewznabCapsAdvertiseLidarrsRequiredParams(t *testing.T) {
	caps := extractOurCaps(t)
	require3(t, strings.Contains(caps, `<search `), "caps has no <search> element")

	search := regexp.MustCompile(`<search [^>]*>`).FindString(caps)
	require3(t, search != "", "caps has no <search> element")

	params := regexp.MustCompile(`supportedParams="([^"]*)"`).FindStringSubmatch(search)
	require3(t, params != nil,
		"<search> carries no supportedParams; Lidarr's SupportsSearch() then answers false and its t=search tier is dead")
	require3(t, strings.Contains(params[1], "q"), "<search supportedParams=%q must include q", params[1])

	audio := regexp.MustCompile(`<audio-search [^>]*>`).FindString(caps)
	require3(t, audio != "", "caps has no <audio-search> element")
	ap := regexp.MustCompile(`supportedParams="([^"]*)"`).FindStringSubmatch(audio)
	require3(t, ap != nil, "<audio-search> carries no supportedParams")
	for _, want := range []string{"q", "artist", "album"} {
		require3(t, strings.Contains(ap[1], want),
			"<audio-search supportedParams=%q must include %s or Lidarr rejects the indexer", ap[1], want)
	}
}

// --- repo readers ---

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require3(t, ok, "cannot locate this test file")
	b, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", rel))
	require3(t, err == nil, "read %s: %v", rel, err)
	return string(b)
}

func extractOurVersionAnswer(t *testing.T) string {
	t.Helper()
	src := repoFile(t, "internal/api/sabnzbd/version.go")
	m := regexp.MustCompile(`const emulatedVersion = "([^"]+)"`).FindStringSubmatch(src)
	require3(t, m != nil, "emulatedVersion not found in version.go")
	return m[1]
}

// extractOurNewznabModes reads the t= values the Newznab dispatch switch
// handles, from the real source rather than from a copy of the list.
func extractOurNewznabModes(t *testing.T) []string {
	t.Helper()
	src := repoFile(t, "internal/api/newznab/handler.go")
	switchStart := strings.Index(src, "switch t {")
	require3(t, switchStart >= 0, "the Newznab dispatch switch moved; update this extractor")
	rest := src[switchStart:]
	end := strings.Index(rest, "\n\t}")
	if end < 0 {
		end = len(rest)
	}
	var modes []string
	for _, m := range regexp.MustCompile(`case "([\w]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		modes = append(modes, m[1])
	}
	require3(t, len(modes) > 0, "no t= cases found in the Newznab dispatch switch")
	return modes
}

// extractOurCaps renders the caps document the way the server does, rather
// than re-reading the template.
func extractOurCaps(t *testing.T) string {
	t.Helper()
	return string(indexer.CapsXML("http://caps.test", "apicompat"))
}

func require3(t *testing.T, ok bool, format string, args ...interface{}) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
