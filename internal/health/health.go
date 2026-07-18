// Package health implements the checks backing the proxy's /health
// endpoint: DB connectivity, CLI binary presence/executability, and free
// disk space. Consumed by Docker's healthcheck, not by Lidarr.
package health

import (
	"database/sql"
	"os"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

const minFreeDiskGB = 1.0

type Result struct {
	Healthy      bool
	FailedChecks []string
}

func Check(db *sql.DB, cliPath string, st *storage.Storage) Result {
	var failed []string

	if err := db.Ping(); err != nil {
		failed = append(failed, "database")
	}

	if info, err := os.Stat(cliPath); err != nil || info.Mode()&0111 == 0 {
		failed = append(failed, "cli_executable")
	}

	if free, _, err := st.GetDiskSpace(); err != nil || free < minFreeDiskGB {
		failed = append(failed, "disk_space")
	}

	return Result{
		Healthy:      len(failed) == 0,
		FailedChecks: failed,
	}
}
