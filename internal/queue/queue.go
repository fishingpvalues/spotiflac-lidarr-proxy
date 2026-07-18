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
			is_history INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_spotify_url ON jobs(spotify_url, is_history, status);
		`
	_, err := db.Exec(query)
	return err
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
	job := &Job{}
	var completedAt sql.NullTime
	err := q.db.QueryRow(
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality, track_count
		 FROM jobs WHERE nzo_id = ? AND is_history = 0`, nzoID,
	).Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status, &job.Category,
		&job.Priority, &job.Filename, &job.OutputPath, &job.Size, &job.Sizeleft,
		&job.Percentage, &job.TimeAdded, &completedAt, &job.ErrorMessage,
		&job.Service, &job.Quality, &job.TrackCount)
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return job, nil
}

// FindActiveBySpotifyURL returns the first non-terminal (Queued or
// Downloading), non-history job matching the given Spotify URL, if any.
func (q *SQLiteQueue) FindActiveBySpotifyURL(url string) (*Job, error) {
	job := &Job{}
	var completedAt sql.NullTime
	err := q.db.QueryRow(
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality, track_count
		 FROM jobs
		 WHERE spotify_url = ? AND is_history = 0 AND status IN (?, ?)
		 ORDER BY time_added ASC LIMIT 1`,
		url, sabnzbd.StatusQueued, sabnzbd.StatusDownloading,
	).Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status, &job.Category,
		&job.Priority, &job.Filename, &job.OutputPath, &job.Size, &job.Sizeleft,
		&job.Percentage, &job.TimeAdded, &completedAt, &job.ErrorMessage,
		&job.Service, &job.Quality, &job.TrackCount)
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return job, nil
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
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality, track_count
		 FROM jobs %s ORDER BY time_added ASC LIMIT ? OFFSET ?`, whereClause)

	allArgs := append(args, params.Limit, params.Start)
	rows, err := q.db.Query(query, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		job := &Job{}
		var completedAt sql.NullTime
		if err := rows.Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status,
			&job.Category, &job.Priority, &job.Filename, &job.OutputPath,
			&job.Size, &job.Sizeleft, &job.Percentage, &job.TimeAdded,
			&completedAt, &job.ErrorMessage, &job.Service, &job.Quality, &job.TrackCount); err != nil {
			return nil, 0, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
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
		        service=?, quality=?, track_count=?
		 WHERE nzo_id=?`,
		job.Status, job.Category, job.Priority, job.Filename, job.OutputPath,
		job.Size, job.Sizeleft, job.Percentage, job.CompletedAt, job.ErrorMessage,
		job.Service, job.Quality, job.TrackCount, job.NzoID,
	)
	return err
}

func (q *SQLiteQueue) Delete(nzoID string, delFiles bool) error {
	_, err := q.db.Exec("DELETE FROM jobs WHERE nzo_id = ?", nzoID)
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
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality, track_count
		 FROM jobs %s ORDER BY completed_at DESC LIMIT ? OFFSET ?`, whereClause)

	allArgs := append(args, params.Limit, params.Start)
	rows, err := q.db.Query(query, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		job := &Job{}
		var completedAt sql.NullTime
		if err := rows.Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status,
			&job.Category, &job.Priority, &job.Filename, &job.OutputPath,
			&job.Size, &job.Sizeleft, &job.Percentage, &job.TimeAdded,
			&completedAt, &job.ErrorMessage, &job.Service, &job.Quality, &job.TrackCount); err != nil {
			return nil, 0, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}

// RecoverStuckJobs marks any job left in Downloading status (from a prior
// crash or unclean restart) as Failed and moves it to history. Called once
// at startup — partial on-disk state from a killed subprocess is never
// trusted or auto-resumed. Runs as a single atomic UPDATE so a mid-sweep
// failure can't leave some jobs recovered and others not, and can't strand
// a job between "marked Failed" and "moved to history".
func (q *SQLiteQueue) RecoverStuckJobs() (int, error) {
	result, err := q.db.Exec(
		`UPDATE jobs SET status = ?, error_message = ?, is_history = 1
		 WHERE status = ? AND is_history = 0`,
		sabnzbd.StatusFailed, "interrupted by restart", sabnzbd.StatusDownloading,
	)
	if err != nil {
		return 0, fmt.Errorf("recover stuck jobs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count recovered jobs: %w", err)
	}
	return int(affected), nil
}

func (q *SQLiteQueue) Close() error {
	return q.db.Close()
}
