package sabnzbd_test

import (
	"os"
	"testing"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
)

// The retry schedule is real time.Sleep, and the tests that exercise the
// retry-then-fall-back-to-another-service path pay it several times over.
// Shortening it here takes the package from 605s to seconds without changing
// what any test asserts: attempt counts, breaker state and fallback order are
// unaffected by how long the gaps are.
//
// 605s mattered because `go test` defaults to a 600s per-package timeout, so
// the suite passed or failed depending on how loaded the machine was.
func TestMain(m *testing.M) {
	restore := sabnzbd.SetRetryBackoffForTest([]time.Duration{time.Millisecond, time.Millisecond})
	code := m.Run()
	restore()
	os.Exit(code)
}
