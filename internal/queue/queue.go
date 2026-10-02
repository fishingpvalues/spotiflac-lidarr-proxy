package queue

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

type SQLiteQueue struct {
	db *sql.DB
}

func New(dbPath string) (*SQLiteQueue, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// modernc.org/sqlite gives each pooled connection its own private
	// database for a ":memory:" DSN (and serializes writes for file DSNs
	// anyway) — a single connection avoids both concurrent connections
	// silently not seeing each other's tables/rows and SQLITE_BUSY
	// contention between connections.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	q := &SQLiteQueue{db: db}
	if _, err := q.RecoverStuckJobs(); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover stuck jobs: %w", err)
	}

	return q, nil
}

func migrate(db *sql.DB) error {
	query := `
		CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			nzo_id TEXT UNIQUE NOT NULL,
			spotify_url TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'Queued',
			category TEXT NOT NULL DEFAULT 'music-flac-16',
			priority TEXT NOT NULL DEFAULT 'Normal',
			filename TEXT NOT NULL DEFAULT '',
			output_path TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			sizeleft INTEGER NOT NULL DEFAULT 0,
			percentage REAL NOT NULL DEFAULT 0.0,
			time_added DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			error_message TEXT DEFAULT '',
			service TEXT NOT NULL DEFAULT '',
			quality TEXT NOT NULL DEFAULT '',
			track_count INTEGER NOT NULL DEFAULT 0,
			cli_output TEXT NOT NULL DEFAULT '',
			is_history INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_spotify_url ON jobs(spotify_url, is_history, status);
		`
	if _, err := db.Exec(query); err != nil {
		return err
	}
	return addMissingColumns(db)
}

// addMissingColumns adds any columns to the jobs table that don't yet exist,
// for databases created before those columns were introduced (e.g. a
// production /data/queue.db from a version of this schema that predates
// track_count/cli_output). CREATE TABLE IF NOT EXISTS is a no-op against an
// already-existing table, so without this step an old database would keep
// missing these columns forever and every subsequent query referencing them
// would fail at runtime with "no such column". Safe to run on every startup:
// each ALTER is skipped if the column is already present (which is always
// the case on a freshly created database, since CREATE TABLE above already
// includes these columns).
func addMissingColumns(db *sql.DB) error {
	existing, err := existingColumns(db)
	if err != nil {
		return fmt.Errorf("read table info: %w", err)
	}

	additions := []struct {
		name string
		ddl  string
	}{
		{"track_count", "ALTER TABLE jobs ADD COLUMN track_count INTEGER NOT NULL DEFAULT 0"},
		{"cli_output", "ALTER TABLE jobs ADD COLUMN cli_output TEXT NOT NULL DEFAULT ''"},
		{"started_at", "ALTER TABLE jobs ADD COLUMN started_at DATETIME"},
		{"progress_at", "ALTER TABLE jobs ADD COLUMN progress_at DATETIME"},
		{"recover_count", "ALTER TABLE jobs ADD COLUMN recover_count INTEGER NOT NULL DEFAULT 0"},
	}
	for _, a := range additions {
		if existing[a.name] {
			continue
		}
		if _, err := db.Exec(a.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", a.name, err)
		}
	}
	return nil
}

// jobColumns is the SELECT list every read path shares. It exists because
// the same 17-column list was copied into four queries and each copy had
// drifted: List never selected cli_output, so a job's backend output - the
// only record of why it failed - was invisible in the queue, and Get never
// did either, so handlePause re-saved a job and silently erased it.
const jobColumns = `id, nzo_id, spotify_url, status, category, priority, filename,
	output_path, size, sizeleft, percentage, time_added, completed_at,
	error_message, service, quality, track_count, cli_output,
	started_at, progress_at, recover_count`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanJob reads one row selected with jobColumns.
func scanJob(row rowScanner) (*Job, error) {
	job := &Job{}
	var completedAt, startedAt, progressAt sql.NullTime
	if err := row.Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status,
		&job.Category, &job.Priority, &job.Filename, &job.OutputPath,
		&job.Size, &job.Sizeleft, &job.Percentage, &job.TimeAdded,
		&completedAt, &job.ErrorMessage, &job.Service, &job.Quality,
		&job.TrackCount, &job.CLIOutput, &startedAt, &progressAt,
		&job.RecoverCount); err != nil {
		return nil, err
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if progressAt.Valid {
		job.ProgressAt = &progressAt.Time
	}
	return job, nil
}

func existingColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func (q *SQLiteQueue) Add(job *Job) error {
	job.TimeAdded = time.Now()
	job.Status = sabnzbd.StatusQueued
	_, err := q.db.Exec(
		`INSERT INTO jobs (nzo_id, spotify_url, status, category, priority, filename, output_path, size, sizeleft, percentage, time_added, service, quality, track_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.NzoID, job.SpotifyURL, job.Status, job.Category, job.Priority,
		job.Filename, job.OutputPath, job.Size, job.Sizeleft, job.Percentage,
		job.TimeAdded, job.Service, job.Quality, job.TrackCount,
	)
	return err
}

func (q *SQLiteQueue) Get(nzoID string) (*Job, error) {
	row := q.db.QueryRow(
		`SELECT `+jobColumns+`
		 FROM jobs WHERE nzo_id = ? AND is_history = 0`, nzoID)
	return scanJob(row)
}

// FindActiveBySpotifyURL returns the first non-terminal (Queued or
// Downloading), non-history job matching the given Spotify URL, if any.
func (q *SQLiteQueue) FindActiveBySpotifyURL(url string) (*Job, error) {
	row := q.db.QueryRow(
		`SELECT `+jobColumns+`
		 FROM jobs
		 WHERE spotify_url = ? AND is_history = 0 AND status IN (?, ?)
		 ORDER BY time_added ASC LIMIT 1`,
		url, sabnzbd.StatusQueued, sabnzbd.StatusDownloading)
	return scanJob(row)
}

func (q *SQLiteQueue) List(params ListParams) ([]*Job, int, error) {
	where := []string{"is_history = 0"}
	args := []interface{}{}

	if params.Search != "" {
		where = append(where, "filename LIKE ?")
		args = append(args, "%"+params.Search+"%")
	}
	if len(params.NzoIDs) > 0 {
		placeholders := make([]string, len(params.NzoIDs))
		for i, id := range params.NzoIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, fmt.Sprintf("nzo_id IN (%s)", strings.Join(placeholders, ",")))
	}
	if params.Status != "" {
		where = append(where, "status = ?")
		args = append(args, params.Status)
	}
	// Category used to be silently dropped here: ListParams carries it and
	// handleQueue passes it, but the WHERE clause never referenced it, so a
	// queue poll filtered by category (Lidarr sends ?category=music on every
	// poll) returned every job regardless of category.
	if params.Category != "" {
		where = append(where, "category = ?")
		args = append(args, params.Category)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM jobs %s", whereClause)
	if err := q.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count query: %w", err)
	}

	if params.Limit == 0 {
		params.Limit = 50
	}

	query := fmt.Sprintf(
		`SELECT `+jobColumns+`
		 FROM jobs %s ORDER BY time_added ASC LIMIT ? OFFSET ?`, whereClause)

	allArgs := append(args, params.Limit, params.Start)
	rows, err := q.db.Query(query, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}

func (q *SQLiteQueue) Update(job *Job) error {
	_, err := q.db.Exec(
		`UPDATE jobs SET status=?, category=?, priority=?, filename=?, output_path=?,
		        size=?, sizeleft=?, percentage=?, completed_at=?, error_message=?,
		        service=?, quality=?, track_count=?, cli_output=?,
		        started_at=?, progress_at=?, recover_count=?
		 WHERE nzo_id=?`,
		job.Status, job.Category, job.Priority, job.Filename, job.OutputPath,
		job.Size, job.Sizeleft, job.Percentage, job.CompletedAt, job.ErrorMessage,
		job.Service, job.Quality, job.TrackCount, job.CLIOutput,
		job.StartedAt, job.ProgressAt, job.RecoverCount, job.NzoID,
	)
	return err
}

func (q *SQLiteQueue) Delete(nzoID string, delFiles bool) error {
	_, err := q.db.Exec("DELETE FROM jobs WHERE nzo_id = ?", nzoID)
	return err
}

// GetByNzoID returns a job regardless of whether it is queued or in history.
//
// Get deliberately filters `is_history = 0` - it serves the queue endpoints -
// but mode=retry exists to re-run a FAILED download, which by definition
// lives in history, so it needs this one.
func (q *SQLiteQueue) GetByNzoID(nzoID string) (*Job, error) {
	row := q.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE nzo_id = ?`, nzoID)
	return scanJob(row)
}

// DeleteFromHistory removes a row only if it is in history. The history
// delete endpoint used to call Delete, which has no is_history predicate, so
// naming an active job deleted the active row out from under its worker.
func (q *SQLiteQueue) DeleteFromHistory(nzoID string) error {
	_, err := q.db.Exec("DELETE FROM jobs WHERE nzo_id = ? AND is_history = 1", nzoID)
	return err
}

// MoveToQueue takes a row out of history and back into the active queue, so
// mode=retry produces a job that queue listings and Rec/Resume logic can see
// again. Update alone never cleared is_history.
func (q *SQLiteQueue) MoveToQueue(nzoID string) error {
	_, err := q.db.Exec("UPDATE jobs SET is_history = 0, recover_count = 0 WHERE nzo_id = ?", nzoID)
	return err
}

func (q *SQLiteQueue) MoveToHistory(nzoID string) error {
	_, err := q.db.Exec("UPDATE jobs SET is_history = 1 WHERE nzo_id = ?", nzoID)
	return err
}

func (q *SQLiteQueue) History(params ListParams) ([]*Job, int, error) {
	where := []string{"is_history = 1"}
	args := []interface{}{}

	if params.Search != "" {
		where = append(where, "filename LIKE ?")
		args = append(args, "%"+params.Search+"%")
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM jobs %s", whereClause)
	if err := q.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count query: %w", err)
	}

	if params.Limit == 0 {
		params.Limit = 50
	}

	query := fmt.Sprintf(
		`SELECT `+jobColumns+`
		 FROM jobs %s ORDER BY completed_at DESC, id DESC LIMIT ? OFFSET ?`, whereClause)

	allArgs := append(args, params.Limit, params.Start)
	rows, err := q.db.Query(query, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}

// RecoverStuckJobs puts every job left in Downloading status back into the
// queue after a crash or unclean restart, and fails only the jobs that have
// already been recovered MaxRecoveries times. Called once at startup.
//
// Requeueing rather than failing is the point. A restart interrupts a
// download; it says nothing about whether the release is downloadable. The
// old behavior - fail and move to history - handed Lidarr a dead grab that
// had to be blocklisted and re-searched, and the only thing that ever
// brought the album back was an external re-grab cycle. Measured over the
// week to 2026-10-02: the DAGU stuck-monitor restarted this container eight
// times, and every in-flight job died "interrupted by restart" even though
// most of those releases had downloaded successfully before. The partial job
// directory is deleted, not trusted: the re-dispatched job starts from an
// empty directory (ProcessDownloadSync -> storage.PrepareJobDir).
//
// Two statements in one transaction, so a mid-sweep failure cannot leave a
// job stranded between "requeued" and "counter bumped", and a job can never
// be failed without its counter having been bumped first.
func (q *SQLiteQueue) RecoverStuckJobs() (int, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("recover stuck jobs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// completed_at has to be set when failing, not left NULL. PruneHistory
	// ranks history by completed_at, and a NULL loses to every timestamped
	// row, so a job recovered into a full history was deleted by the very
	// next addurl instead of being reported as failed. Observed in
	// production: a release Lidarr had grabbed vanished from the queue
	// database entirely, leaving Lidarr with a grab it could neither fail
	// nor blocklist - so it re-grabbed the same release forever.
	result, err := tx.Exec(
		`UPDATE jobs SET status = ?, error_message = ?, is_history = 1, completed_at = ?
		 WHERE status = ? AND is_history = 0 AND recover_count >= ?`,
		sabnzbd.StatusFailed,
		"interrupted by restart, and by more restarts than this job's recovery budget allows",
		time.Now(), sabnzbd.StatusDownloading, MaxRecoveries,
	)
	if err != nil {
		return 0, fmt.Errorf("recover stuck jobs: %w", err)
	}
	failed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count recovered jobs: %w", err)
	}

	result, err = tx.Exec(
		`UPDATE jobs SET status = ?, error_message = '', completed_at = NULL,
		        sizeleft = size, percentage = 0, cli_output = '',
		        started_at = NULL, progress_at = NULL,
		        recover_count = recover_count + 1
		 WHERE status = ? AND is_history = 0 AND recover_count < ?`,
		sabnzbd.StatusQueued, sabnzbd.StatusDownloading, MaxRecoveries,
	)
	if err != nil {
		return 0, fmt.Errorf("requeue interrupted jobs: %w", err)
	}
	requeued, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count requeued jobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("recover stuck jobs: %w", err)
	}
	return int(requeued + failed), nil
}

// PruneHistory deletes history rows beyond the `keep` most recent
// (by completed_at). keep <= 0 disables pruning (unlimited retention).
func (q *SQLiteQueue) PruneHistory(keep int) error {
	if keep <= 0 {
		return nil
	}
	// COALESCE, because completed_at is nullable and a NULL sorts below
	// every real timestamp under DESC - which made any history row without
	// one the first thing deleted, however recent it actually was.
	// time_added is NOT NULL, so it is always a usable stand-in.
	_, err := q.db.Exec(
		`DELETE FROM jobs WHERE is_history = 1 AND id NOT IN (
			SELECT id FROM jobs WHERE is_history = 1
			ORDER BY COALESCE(completed_at, time_added) DESC, id DESC LIMIT ?
		)`, keep,
	)
	if err != nil {
		return fmt.Errorf("prune history: %w", err)
	}
	return nil
}

func (q *SQLiteQueue) Close() error {
	return q.db.Close()
}

// DB exposes the underlying *sql.DB for health checks only.
func (q *SQLiteQueue) DB() *sql.DB {
	return q.db
}
