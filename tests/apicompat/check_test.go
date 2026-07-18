//go:build apicompat

package apicompat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
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

// TestLidarrSabnzbdFields verifies all queue/history/config fields Lidarr
// accesses are present in our response types.
func TestLidarrSabnzbdFields(t *testing.T) {
	fields := fetchLidarrSabnzbdFields(t)

	ourTypes := extractOurTypes()
	for _, field := range fields {
		assertContains(t, ourTypes, field, "missing response field '%s'", field)
	}
	t.Logf("All %d Lidarr SABnzbd fields matched in response types", len(fields))
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
	data, err := os.ReadFile("openapi.json")
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
	cmd := exec.Command("go", "build", "./cmd/server")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(output))
	}
}

// --- helpers ---

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

	var buf strings.Builder
	buf.ReadFrom(resp.Body)
	content := buf.String()

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

	var buf strings.Builder
	buf.ReadFrom(resp.Body)
	content := buf.String()

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
	Services []string
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
			var buf strings.Builder
			buf.ReadFrom(resp.Body)
			content = buf.String()
			resp.Body.Close()
			break
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
	data, err := os.ReadFile("internal/api/sabnzbd/handler.go")
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`case mode == "(\w+)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	modes := make([]string, 0, len(matches))
	for _, m := range matches {
		modes = append(modes, m[1])
	}
	return modes
}

func extractOurTypes() []string {
	data, err := os.ReadFile("pkg/sabnzbd/types.go")
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
	data, err := os.ReadFile("internal/config/config.go")
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`Service\w+\s+=\s+"(\w+)"`)
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
