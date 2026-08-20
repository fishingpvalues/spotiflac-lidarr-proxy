package sabnzbd

import "testing"

func TestSabnzbdVersionOnlyEverAnswersSomethingLidarrParses(t *testing.T) {
	// Lidarr's Sabnzbd.TestConnection fails the whole download client on
	// anything that is neither strict X.Y.Z nor "develop" - measured against
	// a real Lidarr, which answered the Test button with HTTP 400 and
	// "Unknown Version: beta-65825a5". Every non-release tag hit that.
	cases := map[string]string{
		"3.0.0":         "3.0.0",
		"2.14.7":        "2.14.7",
		"develop":       "develop",
		"beta-65825a5":  "develop",
		"latest":        "develop",
		"v3.0.0":        "develop", // a leading v is not X.Y.Z either
		"3.0":           "develop",
		"3.0.0-rc1":     "develop",
		"":              "develop",
		"dev-abcdef123": "develop",
	}
	for in, want := range cases {
		if got := sabnzbdVersion(in); got != want {
			t.Errorf("sabnzbdVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
