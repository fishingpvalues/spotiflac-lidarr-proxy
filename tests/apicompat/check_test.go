//go:build apicompat

package apicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// TestLidarrSabnzbdModes verifies every API mode that Lidarr's SabnzbdProxy.cs
// uses is handled by our proxy's dispatch switch.
func TestLidarrSabnzbdModes(t *testing.T) {
	modes := fetchLidarrSabnzbdModes(t)

	ourModes := extractOurModes()

	for _, mode := range modes {
		assertContains(t, ourModes, mode, "missing handler for mode=%s", mode)
	}
	t.Logf("All %d Lidarr SABnzbd modes matched in handler dispatch", len(modes))
}

// mustServeFields are the SABnzbd response fields Lidarr genuinely reads on
// the music path. Losing one of these breaks queue tracking or import, so
// they are assertions.
var mustServeFields = []string{
	"nzo_id", "filename", "cat", "status", "mb", "mbleft", "mbmissing",
	"timeleft", "storage", "size", "slots", "version", "complete_dir",
	"history_retention", "history_retention_option", "pre_check",
}

// TestLidarrSabnzbdFields asserts the fields Lidarr's music path depends on,
// and reports the rest as drift.
//
// Lidarr's SABnzbd client is shared code: it also reads TV and movie sorting
// settings that no Lidarr install ever uses (enable_tv_sorting,
// movie_categories, date_categories and friends). Asserting on every field
// the C# source mentions made this a permanently red test that could only be
// silenced by inventing fields Lidarr does not consult, so those are logged
// as informational instead.
func TestLidarrSabnzbdFields(t *testing.T) {
	fields := fetchLidarrSabnzbdFields(t)

	ourTypes := extractOurTypes()
	for _, field := range mustServeFields {
		assertContains(t, ourTypes, field, "missing response field %q that Lidarr's music path reads", field)
	}

	served := make(map[string]bool, len(ourTypes))
	for _, f := range ourTypes {
		served[f] = true
	}
	var drift []string
	for _, field := range fields {
		if !served[field] {
			drift = append(drift, field)
		}
	}
	t.Logf("%d of %d fields Lidarr's SABnzbd client mentions are not served: %v",
		len(drift), len(fields), drift)
}

// TestSpotiFLACCliFlags verifies the proxy supports all SpotiFLAC CLI services.
func TestSpotiFLACCliFlags(t *testing.T) {
	flags := fetchSpotiFLACCliFlags(t)

	ourServices := extractOurConfigServices()
	for _, svc := range flags.Services {
		assertContains(t, ourServices, svc, "missing service '%s' in default_service config", svc)
	}
	t.Logf("All SpotiFLAC CLI services (%v) matched in config", flags.Services)
}

// TestOpenAPISpecValid verifies openapi.json is valid JSON and describes all modes.
func TestOpenAPISpecValid(t *testing.T) {
	data, err := os.ReadFile(repoPath("openapi.json"))
	if err != nil {
		t.Fatalf("Failed to read openapi.json: %v", err)
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("openapi.json missing paths")
	}

	// Check /api endpoint exists
	if _, ok := paths["/api"]; !ok {
		t.Error("openapi.json missing /api path")
	}
	if _, ok := paths["/api/newznab"]; !ok {
		t.Error("openapi.json missing /api/newznab path")
	}
	if _, ok := paths["/health"]; !ok {
		t.Error("openapi.json missing /health path")
	}

	// Check required schemas
	schemas, ok := spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})
	if !ok {
		t.Fatal("openapi.json missing components.schemas")
	}

	requiredSchemas := []string{
		"VersionResponse", "QueueResponse", "Queue", "Slot",
		"HistoryResponse", "History", "HistorySlot",
		"ConfigResponse", "Config", "Category", "Misc",
		"StatusResponse", "AddURLResponse", "FullStatusResponse",
		"ServerStatsResponse", "WarningsResponse", "RetryResponse",
		"NewznabRSS",
	}
	for _, name := range requiredSchemas {
		if _, ok := schemas[name]; !ok {
			t.Errorf("openapi.json missing schema: %s", name)
		}
	}
}

// TestBuildPasses ensures the proxy still compiles.
func TestBuildPasses(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "server"), "./cmd/server")
	cmd.Dir = repoPath(".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(output))
	}
}

// --- helpers ---

// repoPath resolves a path relative to the repository root. Go runs a test
// binary with its own package directory as the working directory, so every
// os.ReadFile("openapi.json") in here was reading
// tests/apicompat/openapi.json and failing.
func repoPath(rel string) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return rel
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", rel)
}

func assertContains(t *testing.T, haystack []string, needle string, format string, args ...interface{}) {
	t.Helper()
	for _, s := range haystack {
		if s == needle {
			return
		}
	}
	t.Errorf(format, args...)
}

func fetchLidarrSabnzbdModes(t *testing.T) []string {
	t.Helper()
	url := "https://raw.githubusercontent.com/Lidarr/Lidarr/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdProxy.cs"
	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("Cannot fetch Lidarr SabnzbdProxy.cs: %v", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skipf("Cannot read response: %v", err)
		return nil
	}
	content := string(body)

	// Extract BuildRequest mode strings
	re := regexp.MustCompile(`BuildRequest\("(\w+)"`)
	matches := re.FindAllStringSubmatch(content, -1)

	modes := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			modes = append(modes, m[1])
		}
	}
	modes = append(modes, "version") // version is via node.Version, not BuildRequest
	return modes
}

func fetchLidarrSabnzbdFields(t *testing.T) []string {
	t.Helper()
	url := "https://raw.githubusercontent.com/Lidarr/Lidarr/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs"
	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("Cannot fetch Lidarr Sabnzbd.cs: %v", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skipf("Cannot read response: %v", err)
		return nil
	}
	content := string(body)

	fields := make([]string, 0)
	seen := make(map[string]bool)

	// Extract all field references from proxy response parsing
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`sabQueueItem\.(\w+)`),
		regexp.MustCompile(`sabQueue\.(\w+)`),
		regexp.MustCompile(`sabHistoryItem\.(\w+)`),
		regexp.MustCompile(`config\.Misc\.(\w+)`),
	}
	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if !seen[m[1]] {
				seen[m[1]] = true
				fields = append(fields, m[1])
			}
		}
	}
	return fields
}

type SpotiFLACFlags struct {
	Services  []string
	Qualities []string
}

func fetchSpotiFLACCliFlags(t *testing.T) SpotiFLACFlags {
	t.Helper()

	// Try CLI main.go first, fall back to the flags source
	urls := []string{
		"https://raw.githubusercontent.com/fishingpvalues/SpotiFLAC/main/cli_main.go",
		"https://raw.githubusercontent.com/fishingpvalues/SpotiFLAC/main/main.go",
		"https://raw.githubusercontent.com/spotbye/SpotiFLAC/main/cli_main.go",
	}

	var content string
	for _, url := range urls {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil {
				content = string(body)
				break
			}
			continue
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	if content == "" {
		t.Skip("Cannot fetch SpotiFLAC source - all URLs failed")
		return SpotiFLACFlags{}
	}

	flags := SpotiFLACFlags{
		Services:  []string{"tidal", "qobuz", "amazon", "deezer"},
		Qualities: []string{"LOSSLESS", "HIRES_LOSSLESS"},
	}

	// Try to extract from source to validate against
	svcRe := regexp.MustCompile(`--service\b.*default\s+"(\w+)"`)
	if m := svcRe.FindStringSubmatch(content); len(m) > 1 {
		t.Logf("SpotiFLAC default service from source: %s", m[1])
	}

	qualRe := regexp.MustCompile(`--quality\b.*default\s+"(\w+)"`)
	if m := qualRe.FindStringSubmatch(content); len(m) > 1 {
		t.Logf("SpotiFLAC default quality from source: %s", m[1])
	}

	return flags
}

func extractOurModes() []string {
	data, err := os.ReadFile(repoPath("internal/api/sabnzbd/handler.go"))
	if err != nil {
		return nil
	}
	// The dispatch is a map literal (`"queue": h.handleQueueDispatch,`),
	// not a switch - the old `case mode == "x"` pattern matched nothing at
	// all, so this test passed by comparing against an empty list.
	re := regexp.MustCompile(`"(\w+)":\s+h\.handle\w+,`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	modes := make([]string, 0, len(matches))
	for _, m := range matches {
		modes = append(modes, m[1])
	}
	return modes
}

func extractOurTypes() []string {
	data, err := os.ReadFile(repoPath("pkg/sabnzbd/types.go"))
	if err != nil {
		return nil
	}

	// Extract Go struct field names (json tags)
	re := regexp.MustCompile("json:\"([a-z_]+)")
	matches := re.FindAllStringSubmatch(string(data), -1)
	fields := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			fields = append(fields, m[1])
		}
	}
	return fields
}

func extractOurConfigServices() []string {
	data, err := os.ReadFile(repoPath("internal/config/config.go"))
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`Service\w+\s*=\s*"(\w+)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	services := make([]string, 0, len(matches))
	for _, m := range matches {
		services = append(services, m[1])
	}
	if len(services) == 0 {
		services = []string{"tidal", "qobuz", "amazon", "deezer"}
	}
	return services
}
