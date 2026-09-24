package health

import (
	"os"
	"path/filepath"
	"strings"
)

// Backend is what BackendWarnings reads to decide whether a download can
// succeed at all. Every field is plain configuration, so the check is cheap
// enough to run on each /health and mode=warnings request.
type Backend struct {
	// PythonBin is the SpotiFLAC venv's interpreter; empty or missing means
	// the Python backend is not installed and its extensions do not matter.
	PythonBin string
	// HomeDir holds ~/.spotiflac/extensions and ~/.spotiflac_env.
	HomeDir string
	// Registries is SPOTIFLAC_REGISTRIES.
	Registries string
	// Any one of these gets a community verification challenge answered:
	// byparr/FlareSolverr and the session-renewal command solve it
	// unattended, the relay carries a human's browser answer back to the
	// container. Without one, the challenge's callback points at a loopback
	// port inside the container that no browser can reach.
	FSLURL          string
	VerifyRelayURL  string
	SessionRenewCmd string
}

// Backend warning IDs, stable for mode=warnings consumers.
const (
	WarnNoExtensions  = "backend_no_extensions"
	WarnNoVerifySolve = "backend_no_verification_solver"
)

// BackendWarning is one reason downloads cannot complete.
type BackendWarning struct {
	ID   string
	Text string
}

// BackendWarnings reports configuration under which downloads cannot
// complete, even though /health is otherwise "ok" and Lidarr's Tests pass.
//
// Measured against the stock 0.0.3 image with nothing but SPF_API_KEY set:
// two albums sat at "Downloading 0%" for eight minutes and produced no file.
// SpotiFLAC 3.x installs its download extensions from a registry, and with no
// SPOTIFLAC_REGISTRIES none were ever installed ("No extensions found for:
// [tidal]"), so the Python backend failed at once. The CLI's community tier
// then asked for a Turnstile verification that nothing could answer, and timed
// out after five minutes per attempt. Neither showed up in /health or
// mode=warnings.
func BackendWarnings(b Backend) []BackendWarning {
	var out []BackendWarning

	if pythonInstalled(b.PythonBin) && !extensionsAvailable(b) {
		out = append(out, BackendWarning{
			ID: WarnNoExtensions,
			Text: "SpotiFLAC Python backend has no download extensions: SPOTIFLAC_REGISTRIES is not set " +
				"and ~/.spotiflac/extensions is empty, so every download falls through to the CLI. " +
				"Set SPOTIFLAC_REGISTRIES to a registry.json URL (see README, \"How it works\")",
		})
	}

	if b.FSLURL == "" && b.VerifyRelayURL == "" && b.SessionRenewCmd == "" {
		out = append(out, BackendWarning{
			ID: WarnNoVerifySolve,
			Text: "no community verification solver configured (SPOTIFLAC_FSL_URL, SPF_VERIFY_RELAY_URL " +
				"or SPF_SESSION_RENEW_CMD): when the community tier asks for a Turnstile check, " +
				"the download waits for it and times out",
		})
	}

	return out
}

func pythonInstalled(bin string) bool {
	if bin == "" {
		return false
	}
	_, err := os.Stat(bin)
	return err == nil
}

func extensionsAvailable(b Backend) bool {
	if strings.TrimSpace(b.Registries) != "" {
		return true
	}
	if b.HomeDir == "" {
		return false
	}
	// SpotiFLAC also reads the registry from ~/.spotiflac_env.
	if data, err := os.ReadFile(filepath.Join(b.HomeDir, ".spotiflac_env")); err == nil &&
		strings.Contains(string(data), "SPOTIFLAC_REGISTRIES=") {
		return true
	}
	// Extensions installed earlier (or mounted in) work without a registry.
	entries, err := os.ReadDir(filepath.Join(b.HomeDir, ".spotiflac", "extensions"))
	return err == nil && len(entries) > 0
}
