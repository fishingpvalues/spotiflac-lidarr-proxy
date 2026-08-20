package sabnzbd

import (
	"context"
	"os/exec"
)

// browserProcessPatterns are the processes a SpotiFLAC download leaves behind:
// the browser its providers drive, that browser's crash handler, and the node
// bridge that hosts the extensions.
var browserProcessPatterns = []string{"chromium", "chrome_crashpad", "_bridge.js"}

// reapStaleBrowsers kills browser processes left over from earlier downloads.
//
// SpotiFLAC does not clean up after itself: a finished job leaves its Chromium
// and the node extension bridge running, and they accumulate. Measured in a
// live container - 21 such processes after a single job had moved on - and the
// consequence is not merely wasted memory: a *new* browser then cannot start
// at all. The same pydoll script that fails with FailedToStartBrowser (or
// starts and then times out on every CDP command) succeeds immediately after
// the strays are killed:
//
//	before reaping   FailedToStartBrowser / CommandExecutionTimeout
//	after reaping    STARTED -> navigated -> 544 bytes -> OK
//
// That is what surfaced as "NetworkMethod.ENABLE, timeout=60s" and then
// "ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download" on every
// download after the first.
//
// Only ever called when at most one job may run at a time, because it cannot
// tell a stray from a sibling job's browser - see the guard in the caller.
// Failures are ignored: nothing to kill is the normal case and is not an
// error.
func reapStaleBrowsers() {
	for _, pattern := range browserProcessPatterns {
		_ = exec.Command("pkill", "-9", "-f", pattern).Run()
	}
}

// CancelJob stops an in-flight download, if there is one for this nzo_id.
// Safe to call for a job that is not running.
func (h *Handler) CancelJob(nzoID string) bool {
	if v, ok := h.running.LoadAndDelete(nzoID); ok {
		if cancel, ok := v.(context.CancelFunc); ok {
			cancel()
			return true
		}
	}
	return false
}
