package spotiflac

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindPythonHonoursOrRefusesAConfiguredPath guards a silent fallback.
// A configured-but-missing interpreter used to fall through to the well-known
// locations, so SPF_SPOTIFLAC_PYTHON_VENV pointing at a nonexistent path ran a
// different interpreter and the setting appeared applied while changing
// nothing. Observed in a live container: with the venv set to
// /nonexistent/python3 the wrapper still ran under /venv/bin/python3.
func TestFindPythonHonoursOrRefusesAConfiguredPath(t *testing.T) {
	real := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := findPython(real); got != real {
		t.Errorf("configured interpreter that exists: got %q, want %q", got, real)
	}

	if got := findPython("/nonexistent/python3"); got != "" {
		t.Errorf("configured interpreter that is missing: got %q, want \"\" "+
			"(a fallback would silently run a different interpreter)", got)
	}

	// With nothing configured, probing the well-known locations is still right.
	if got := findPython(""); got == "" {
		t.Error("unconfigured: expected a probed fallback, got empty")
	}
}
