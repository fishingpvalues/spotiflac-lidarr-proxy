package spotiflac

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed python_wrapper/spotiflac-py-wrapper.py
var pythonWrapperFS embed.FS

// extractPythonWrapper writes the embedded Python wrapper script to a temp
// file and returns its path. The caller should clean it up when done.
func extractPythonWrapper() (string, error) {
	data, err := pythonWrapperFS.ReadFile("python_wrapper/spotiflac-py-wrapper.py")
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "spotiflac-py-wrapper")
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, "spotiflac-py-wrapper.py")
	if err := os.WriteFile(path, data, 0755); err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	return path, nil
}

// findPython returns the Python binary to run the wrapper with, or "" when
// there is none to use.
//
// An explicitly configured path is honoured or refused - never silently
// replaced. It used to fall through to the well-known locations when the
// configured interpreter was missing, so setting SPF_SPOTIFLAC_PYTHON_VENV to
// a path that does not exist ran a *different* interpreter instead: the
// setting looked applied and changed nothing. Verified in a live container -
// with the venv pointed at /nonexistent/python3, the wrapper still ran under
// /venv/bin/python3.
//
// Refusing is the useful behaviour: an operator naming an interpreter is
// either pointing at a specific environment or deliberately disabling the
// Python backend, and a fallback defeats both.
func findPython(venvPath string) string {
	if venvPath != "" {
		if _, err := os.Stat(venvPath); err == nil {
			return venvPath
		}
		return ""
	}

	// Common locations
	for _, p := range []string{
		"/venv/bin/python3",
		"/app/venv/bin/python3",
		"/usr/local/bin/python3",
		"/usr/bin/python3",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return "python3" // fallback to PATH
}
