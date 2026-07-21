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

// findPython returns the best available Python binary path.
// Checks SPOTIFLAC_PYTHON_VENV env, then /venv/bin/python3, then system python3.
func findPython(venvPath string) string {
	if venvPath != "" {
		if _, err := os.Stat(venvPath); err == nil {
			return venvPath
		}
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
