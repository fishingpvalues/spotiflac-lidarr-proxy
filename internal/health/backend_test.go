package health

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ids(ws []BackendWarning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

// fakePython stands in for the venv interpreter; only its existence matters.
func fakePython(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(p, nil, 0o755))
	return p
}

// The stock image as issue #7's reporter ran it: Python backend installed,
// no registry, nothing to solve a verification.
func TestBackendWarningsStockImage(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".spotiflac", "extensions"), 0o755))

	got := BackendWarnings(Backend{PythonBin: fakePython(t), HomeDir: home})
	assert.Equal(t, []string{WarnNoExtensions, WarnNoVerifySolve}, ids(got))
}

func TestBackendWarningsConfigured(t *testing.T) {
	got := BackendWarnings(Backend{
		PythonBin:  fakePython(t),
		HomeDir:    t.TempDir(),
		Registries: "https://example.invalid/registry.json",
		FSLURL:     "http://byparr:8191/v1",
	})
	assert.Empty(t, got)
}

func TestBackendWarningsExtensionSources(t *testing.T) {
	py := fakePython(t)

	t.Run("spotiflac_env file", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(home, ".spotiflac_env"),
			[]byte("SPOTIFLAC_REGISTRIES=https://example.invalid/registry.json\n"), 0o600))
		assert.NotContains(t, ids(BackendWarnings(Backend{PythonBin: py, HomeDir: home})), WarnNoExtensions)
	})

	t.Run("extensions already installed", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".spotiflac", "extensions", "tidal-web"), 0o755))
		assert.NotContains(t, ids(BackendWarnings(Backend{PythonBin: py, HomeDir: home})), WarnNoExtensions)
	})

	t.Run("no Python backend at all", func(t *testing.T) {
		got := BackendWarnings(Backend{PythonBin: "/nonexistent/python3", HomeDir: t.TempDir()})
		assert.NotContains(t, ids(got), WarnNoExtensions, "extensions only matter when the Python backend exists")
	})
}

func TestBackendWarningsAnySolverSilencesVerification(t *testing.T) {
	for name, b := range map[string]Backend{
		"fsl":   {FSLURL: "http://byparr:8191/v1"},
		"relay": {VerifyRelayURL: "https://relay.example/verify/callback"},
		"renew": {SessionRenewCmd: "python3 /opt/solver/turnstile-llm-solve.py 10"},
	} {
		assert.NotContains(t, ids(BackendWarnings(b)), WarnNoVerifySolve, name)
	}
}
