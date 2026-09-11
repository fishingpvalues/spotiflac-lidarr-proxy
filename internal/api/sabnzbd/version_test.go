package sabnzbd

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Lidarr parses mode=version with these two rules and fails the whole
// download client when either is broken, so no build string may ever reach
// the answer. Both were measured against a real Lidarr 3.1.5.5066:
// "Unknown Version: beta-65825a5" for an unparseable one, and
// "Version 0.7.0+ is required, but found 0.0.3" for this project's own.
func TestSabnzbdVersionAlwaysAnswersSomethingLidarrAccepts(t *testing.T) {
	semver := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

	for _, build := range []string{
		"v0.0.3", "0.0.3", "v1.2.3", "develop", "latest",
		"beta-65825a5", "dev-abcdef123", "3.0", "3.0.0-rc1", "",
	} {
		got := sabnzbdVersion(build)
		m := semver.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("sabnzbdVersion(%q) = %q, which Lidarr cannot parse", build, got)
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major == 0 && minor < 7 {
			t.Fatalf("sabnzbdVersion(%q) = %q, below Lidarr's 0.7.0 minimum", build, got)
		}
		// "develop" parses and clears the minimum, but Lidarr attaches a
		// warning to it and then refuses to save the client without
		// forceSave.
		if strings.Contains(got, "develop") {
			t.Fatalf("sabnzbdVersion(%q) = %q, which Lidarr saves only with a warning", build, got)
		}
	}
}

// history_retention_option, which handleGetConfig sets so Lidarr does not
// raise DownloadClientRemovesCompletedDownloadsCheck, is read on SABnzbd 4.3
// and later. Claiming an older version would make Lidarr ignore it.
func TestEmulatedVersionIsAtLeastTheOneTheConfigEmulationAssumes(t *testing.T) {
	if emulatedVersion < "4.3" {
		t.Fatalf("emulatedVersion = %q, older than the 4.3 config surface handleGetConfig emulates", emulatedVersion)
	}
}
