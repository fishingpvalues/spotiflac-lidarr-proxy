package spotiflac_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// fakePython writes an executable standing in for a Python interpreter.
// exitCode is what it returns for the `-c "import spotiflac"` probe.
func fakePython(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "python3")
	script := "#!/bin/sh\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python: %v", err)
	}
	return path
}

func clientWithPython(python string) *spotiflac.Client {
	return spotiflac.NewClient(
		"/nonexistent/spotiflac-cli", time.Minute,
		"tidal", "lossless", "", "", "", nil, python, nil,
	)
}

// The regression this exists for: HasPythonBackend used to answer "yes" for
// any interpreter that merely EXISTED. The shipped image has
// /venv/bin/python3 symlinked to the system interpreter and no spotiflac
// module, so it answered yes, SupportsService kept deezer in the fallback
// chain, the chain handed deezer to spotiflac-cli, and every such job died
// with `service "deezer" is only available through the Python backend, which
// is not available in this deployment` - the precise error the filter was
// added to prevent. Observed still happening in production on 2026-09-11,
// after the filter had shipped.
func TestPythonBackendAbsentWhenModuleDoesNotImport(t *testing.T) {
	c := clientWithPython(fakePython(t, 1))

	if c.HasPythonBackend() {
		t.Fatal("an interpreter that cannot import spotiflac is not a Python backend")
	}
	if c.SupportsService("deezer") {
		t.Fatal("deezer is Python-only; it must not be offered when the module is missing")
	}
	for _, svc := range []string{"tidal", "qobuz", "amazon"} {
		if !c.SupportsService(svc) {
			t.Fatalf("%s is implemented by spotiflac-cli and must stay supported", svc)
		}
	}
}

func TestPythonBackendPresentWhenModuleImports(t *testing.T) {
	c := clientWithPython(fakePython(t, 0))

	if !c.HasPythonBackend() {
		t.Fatal("an interpreter that imports spotiflac is a usable Python backend")
	}
	if !c.SupportsService("deezer") {
		t.Fatal("deezer must be offered once the Python backend really works")
	}
}

func TestPythonBackendAbsentWhenInterpreterMissing(t *testing.T) {
	c := clientWithPython(filepath.Join(t.TempDir(), "no-such-python"))

	if c.HasPythonBackend() {
		t.Fatal("a missing interpreter is not a Python backend")
	}
	if c.SupportsService("deezer") {
		t.Fatal("deezer must not be offered without an interpreter")
	}
}

// The probe execs a subprocess, so it must be cached - it is consulted on
// every job and on every fallback hop.
func TestPythonBackendProbeIsCached(t *testing.T) {
	python := fakePython(t, 1)
	c := clientWithPython(python)

	if c.HasPythonBackend() {
		t.Fatal("precondition: probe should fail")
	}
	// Make the interpreter succeed AFTER the first probe. A cached answer
	// must not change; an uncached one would start exec'ing per call.
	if err := os.WriteFile(python, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("rewrite fake python: %v", err)
	}
	if c.HasPythonBackend() {
		t.Fatal("probe result must be cached for the life of the client")
	}
}
