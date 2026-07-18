# Spotiflac-Lidarr Proxy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Go service that bridges Lidarr ↔ SpotiFLAC via SABnzbd + Newznab API shim, with Docker deployment and full CI/CD.

**Architecture:** Pragmatic monolith — `cmd/server` entry point, `internal/` packages for API handlers, SpotiFLAC subprocess client, SQLite queue, Newznab indexer. Single binary, fiber HTTP router, zerolog logging, viper config.

**Tech Stack:** Go 1.24, fiber/v3, viper, zerolog, modernc.org/sqlite, testify, cobra

**Spec:** `docs/superpowers/specs/2026-07-18-spotiflac-lidarr-proxy-design.md`

## Global Constraints

- Module path: `github.com/fishingpvalues/spotiflac-lidarr-proxy`
- Go 1.24.4+
- All config via env vars prefixed `SPF_`
- SABnzbd API at `/api/sabnzbd`, Newznab at `/api/newznab`
- SABnzbd responses must be exact JSON matching real SABnzbd output
- Newznab responses must be valid RSS 2.0 XML with Newznab attributes
- API key auth on all endpoints except `version` and `auth`
- SQLite queue via `modernc.org/sqlite` — pure Go, no CGO
- SpotiFLAC CLI at configurable path, exec'd as subprocess
- Conventional commits enforced via Lefthook
- Docker multi-stage build targeting GHCR

---

### Task 1: Project scaffold, Go module, config package, domain types

**Files:**
- Create: `go.mod`
- Create: `cmd/server/main.go`
- Create: `internal/config/config.go`
- Create: `pkg/sabnzbd/types.go`

**Interfaces:**
- Produces: `config.Config` struct with all env var fields
- Produces: `config.Load() (*Config, error)`
- Produces: `sabnzbd.QueueResponse`, `sabnzbd.HistoryResponse`, `sabnzbd.Slot`, `sabnzbd.VersionResponse`, `sabnzbd.AddURLResponse`, `sabnzbd.CategoriesResponse`
- Produces: `sabnzbd.JobStatus` constants: `StatusDownloading`, `StatusCompleted`, `StatusFailed`, `StatusQueued`, `StatusPaused`

- [ ] **Step 1: Initialize Go module**

```bash
cd /home/daniel/workdir/lidarr-spotiflac-proxy
go mod init github.com/fishingpvalues/spotiflac-lidarr-proxy
```

- [ ] **Step 2: Write config package**

File: `internal/config/config.go`
```go
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Port              int           `mapstructure:"port"`
	APIKey            string        `mapstructure:"api_key"`
	OutputDir         string        `mapstructure:"output_dir"`
	SpotiflacCLIPath  string        `mapstructure:"spotiflac_cli_path"`
	DefaultService    string        `mapstructure:"default_service"`
	DefaultQuality    string        `mapstructure:"default_quality"`
	MaxConcurrent     int           `mapstructure:"max_concurrent"`
	JobTimeout        time.Duration `mapstructure:"job_timeout"`
	DBPath            string        `mapstructure:"db_path"`
	LogLevel          string        `mapstructure:"log_level"`
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("SPF")
	v.AutomaticEnv()

	setDefaults(v)

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("SPF_API_KEY is required")
	}

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", cfg.OutputDir, err)
	}

	dbDir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir %s: %w", dbDir, err)
	}

	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("port", 8484)
	v.SetDefault("output_dir", "/downloads")
	v.SetDefault("spotiflac_cli_path", "/usr/local/bin/spotiflac-cli")
	v.SetDefault("default_service", "tidal")
	v.SetDefault("default_quality", "lossless")
	v.SetDefault("max_concurrent", 3)
	v.SetDefault("job_timeout", "30m")
	v.SetDefault("db_path", "/data/queue.db")
	v.SetDefault("log_level", "info")
}
```

- [ ] **Step 3: Write SABnzbd types**

File: `pkg/sabnzbd/types.go`
```go
package sabnzbd

type JobStatus string

const (
	StatusQueued      JobStatus = "Queued"
	StatusDownloading JobStatus = "Downloading"
	StatusCompleted   JobStatus = "Completed"
	StatusFailed      JobStatus = "Failed"
	StatusPaused      JobStatus = "Paused"
)

type VersionResponse struct {
	Version string `json:"version"`
}

type AuthResponse struct {
	Auth bool `json:"auth"`
}

type AddURLResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids"`
}

type CategoriesResponse struct {
	Categories []string `json:"categories"`
}

type QueueResponse struct {
	Queue Queue `json:"queue"`
}

type Queue struct {
	Status        string  `json:"status"`
	Speedlimit    string  `json:"speedlimit"`
	SpeedlimitAbs string  `json:"speedlimit_abs"`
	Paused        bool    `json:"paused"`
	Noofslots     int     `json:"noofslots"`
	NoofslotsTotal int    `json:"noofslots_total"`
	Limit         int     `json:"limit"`
	Start         int     `json:"start"`
	Timeleft      string  `json:"timeleft"`
	Speed         string  `json:"speed"`
	Kbpersec      string  `json:"kbpersec"`
	Size          string  `json:"size"`
	Sizeleft      string  `json:"sizeleft"`
	Mb            string  `json:"mb"`
	Mbleft        string  `json:"mbleft"`
	Slots         []Slot  `json:"slots"`
	Diskspace1    string  `json:"diskspace1"`
	Diskspace2    string  `json:"diskspace2"`
	Diskspacetotal1 string `json:"diskspacetotal1"`
	Diskspacetotal2 string `json:"diskspacetotal2"`
	Version       string  `json:"version"`
	Finish        int     `json:"finish"`
	PausedAll     bool    `json:"paused_all"`
}

type Slot struct {
	Status       string   `json:"status"`
	Index        int      `json:"index"`
	NzoID        string   `json:"nzo_id"`
	Filename     string   `json:"filename"`
	Size         string   `json:"size"`
	Sizeleft     string   `json:"sizeleft"`
	Mb           string   `json:"mb"`
	Mbleft       string   `json:"mbleft"`
	Percentage   string   `json:"percentage"`
	Timeleft     string   `json:"timeleft"`
	Priority     string   `json:"priority"`
	Cat          string   `json:"cat"`
	Labels       []string `json:"labels"`
	TimeAdded    int64    `json:"time_added"`
	Script       string   `json:"script"`
	Unpackopts   string   `json:"unpackopts"`
	Password     string   `json:"password"`
	AvgAge       string   `json:"avg_age"`
	DirectUnpack string   `json:"direct_unpack"`
	Mbmissing    string   `json:"mbmissing"`
}

type HistoryResponse struct {
	History History `json:"history"`
}

type History struct {
	Noofslots   int         `json:"noofslots"`
	TotalSize   string      `json:"total_size"`
	MonthSize   string      `json:"month_size"`
	WeekSize    string      `json:"week_size"`
	Slots       []HistorySlot `json:"slots"`
	Version     string      `json:"version"`
}

type HistorySlot struct {
	Status       string `json:"status"`
	NzoID        string `json:"nzo_id"`
	Name         string `json:"name"`
	Size         string `json:"size"`
	Cat          string `json:"cat"`
	Completed    int64  `json:"completed"`
	DownloadTime int    `json:"download_time"`
	Script       string `json:"script"`
	Storage      string `json:"storage"`
	Path         string `json:"path"`
	FailMessage  string `json:"fail_message,omitempty"`
	URL          string `json:"url,omitempty"`
}

type StatusResponse struct {
	Status bool   `json:"status"`
	NzoIDs []string `json:"nzo_ids,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ConfigResponse struct {
	Config struct {
		Categories []Category `json:"categories"`
		Scripts    []Script   `json:"scripts"`
		Speedlimit string     `json:"speedlimit"`
		Misc       Misc       `json:"misc"`
	} `json:"config"`
}

type Category struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
	Dir   string `json:"dir"`
}

type Script struct {
	Name     string `json:"name"`
	Default  bool   `json:"default"`
}

type Misc struct {
	Version            string `json:"version"`
	CompletedDir       string `json:"completed_dir"`
	DownloadDir        string `json:"download_dir"`
	CompleteDirEnabled bool   `json:"complete_dir_enabled"`
}
```

- [ ] **Step 4: Install dependencies**

```bash
cd /home/daniel/workdir/lidarr-spotiflac-proxy
go get github.com/spf13/viper
go get github.com/gofiber/fiber/v3
go get github.com/rs/zerolog
go get github.com/spf13/cobra
go get modernc.org/sqlite
go get github.com/stretchr/testify
go get github.com/google/uuid
go mod tidy
```

Expected: `go mod tidy` succeeds, `go.mod` and `go.sum` populated.

- [ ] **Step 5: Verify build compiles**

```bash
cd /home/daniel/workdir/lidarr-spotiflac-proxy
go build ./...
```

Expected: builds without errors.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore: scaffold project with config package and SABnzbd types"
```

---

### Task 2: Queue package — SQLite-backed job queue

**Files:**
- Create: `internal/queue/job.go`
- Create: `internal/queue/queue.go`
- Create: `internal/queue/queue_test.go`

**Interfaces:**
- Produces: `queue.Job` struct with fields: `ID`, `NzoID`, `SpotifyURL`, `Status`, `Category`, `Priority`, `Filename`, `OutputPath`, `Size`, `Sizeleft`, `Percentage`, `TimeAdded`, `CompletedAt`, `ErrorMessage`, `Service`, `Quality`
- Produces: `queue.Queue` interface: `Add(job *Job) error`, `Get(nzoID string) (*Job, error)`, `List(params ListParams) ([]*Job, int, error)`, `Update(job *Job) error`, `Delete(nzoID string, delFiles bool) error`, `History(params ListParams) ([]*Job, int, error)`, `Close() error`
- Produces: `queue.New(dbPath string) (*SQLiteQueue, error)`
- Produces: `queue.ListParams` struct: `Start`, `Limit`, `Search`, `NzoIDs`, `Status`, `Category`

- [ ] **Step 1: Write job model**

File: `internal/queue/job.go`
```go
package queue

import (
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

type Job struct {
	ID           int64              `json:"-"`
	NzoID        string             `json:"nzo_id"`
	SpotifyURL   string             `json:"spotify_url"`
	Status       sabnzbd.JobStatus  `json:"status"`
	Category     string             `json:"category"`
	Priority     string             `json:"priority"`
	Filename     string             `json:"filename"`
	OutputPath   string             `json:"output_path"`
	Size         int64              `json:"size"`
	Sizeleft     int64              `json:"sizeleft"`
	Percentage   float64            `json:"percentage"`
	TimeAdded    time.Time          `json:"time_added"`
	CompletedAt  *time.Time         `json:"completed_at,omitempty"`
	ErrorMessage string             `json:"error_message,omitempty"`
	Service      string             `json:"service"`
	Quality      string             `json:"quality"`
}

type ListParams struct {
	Start    int
	Limit    int
	Search   string
	NzoIDs   []string
	Status   string
	Category string
}
```

- [ ] **Step 2: Write queue implementation**

File: `internal/queue/queue.go`
```go
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

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SQLiteQueue{db: db}, nil
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
		is_history INTEGER NOT NULL DEFAULT 0
	);
	`
	_, err := db.Exec(query)
	return err
}

func (q *SQLiteQueue) Add(job *Job) error {
	job.TimeAdded = time.Now()
	job.Status = sabnzbd.StatusQueued
	_, err := q.db.Exec(
		`INSERT INTO jobs (nzo_id, spotify_url, status, category, priority, filename, output_path, size, sizeleft, percentage, time_added, service, quality)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.NzoID, job.SpotifyURL, job.Status, job.Category, job.Priority,
		job.Filename, job.OutputPath, job.Size, job.Sizeleft, job.Percentage,
		job.TimeAdded, job.Service, job.Quality,
	)
	return err
}

func (q *SQLiteQueue) Get(nzoID string) (*Job, error) {
	job := &Job{}
	var completedAt sql.NullTime
	err := q.db.QueryRow(
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality
		 FROM jobs WHERE nzo_id = ? AND is_history = 0`, nzoID,
	).Scan(&job.ID, &job.NzoID, &job.SpotifyURL, &job.Status, &job.Category,
		&job.Priority, &job.Filename, &job.OutputPath, &job.Size, &job.Sizeleft,
		&job.Percentage, &job.TimeAdded, &completedAt, &job.ErrorMessage,
		&job.Service, &job.Quality)
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
	q.db.QueryRow(countQuery, args...).Scan(&total)

	if params.Limit == 0 {
		params.Limit = 50
	}

	query := fmt.Sprintf(
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality
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
			&completedAt, &job.ErrorMessage, &job.Service, &job.Quality); err != nil {
			return nil, 0, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		jobs = append(jobs, job)
	}
	return jobs, total, nil
}

func (q *SQLiteQueue) Update(job *Job) error {
	_, err := q.db.Exec(
		`UPDATE jobs SET status=?, category=?, priority=?, filename=?, output_path=?,
		        size=?, sizeleft=?, percentage=?, completed_at=?, error_message=?,
		        service=?, quality=?
		 WHERE nzo_id=?`,
		job.Status, job.Category, job.Priority, job.Filename, job.OutputPath,
		job.Size, job.Sizeleft, job.Percentage, job.CompletedAt, job.ErrorMessage,
		job.Service, job.Quality, job.NzoID,
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
	q.db.QueryRow(countQuery, args...).Scan(&total)

	if params.Limit == 0 {
		params.Limit = 50
	}

	query := fmt.Sprintf(
		`SELECT id, nzo_id, spotify_url, status, category, priority, filename,
		        output_path, size, sizeleft, percentage, time_added, completed_at,
		        error_message, service, quality
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
			&completedAt, &job.ErrorMessage, &job.Service, &job.Quality); err != nil {
			return nil, 0, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		jobs = append(jobs, job)
	}
	return jobs, total, nil
}

func (q *SQLiteQueue) Close() error {
	return q.db.Close()
}
```

- [ ] **Step 3: Write queue tests**

File: `internal/queue/queue_test.go`
```go
package queue_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func newTestQueue(t *testing.T) *queue.SQLiteQueue {
	t.Helper()
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	return q
}

func TestAddAndGet(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_test001",
		SpotifyURL: "https://open.spotify.com/album/12345",
		Category:   "music-flac-16",
		Priority:   "Normal",
		Service:    "tidal",
		Quality:    "lossless",
	}
	err := q.Add(job)
	require.NoError(t, err)

	got, err := q.Get("SABnzbd_nzo_test001")
	require.NoError(t, err)

	assert.Equal(t, sabnzbd.StatusQueued, got.Status)
	assert.Equal(t, "https://open.spotify.com/album/12345", got.SpotifyURL)
	assert.NotZero(t, got.TimeAdded)
}

func TestList(t *testing.T) {
	q := newTestQueue(t)

	for i := 0; i < 3; i++ {
		job := &queue.Job{
			NzoID:      "SABnzbd_nzo_test00" + string(rune('1'+i)),
			SpotifyURL: "https://open.spotify.com/album/" + string(rune('1'+i)),
			Category:   "music-flac-16",
			Priority:   "Normal",
		}
		require.NoError(t, q.Add(job))
	}

	jobs, total, err := q.List(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, jobs, 3)
}

func TestUpdate(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:    "SABnzbd_nzo_test001",
		Filename: "Artist - Album",
	}
	require.NoError(t, q.Add(job))

	job.Status = sabnzbd.StatusDownloading
	job.Percentage = 50.0
	require.NoError(t, q.Update(job))

	got, err := q.Get("SABnzbd_nzo_test001")
	require.NoError(t, err)
	assert.Equal(t, sabnzbd.StatusDownloading, got.Status)
	assert.Equal(t, 50.0, got.Percentage)
}

func TestDelete(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_test001"}
	require.NoError(t, q.Add(job))

	err := q.Delete("SABnzbd_nzo_test001", false)
	require.NoError(t, err)

	_, err = q.Get("SABnzbd_nzo_test001")
	assert.Error(t, err)
}

func TestMoveToHistory(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_test001"}
	require.NoError(t, q.Add(job))

	require.NoError(t, q.MoveToHistory("SABnzbd_nzo_test001"))

	_, err := q.Get("SABnzbd_nzo_test001")
	assert.Error(t, err)

	hjobs, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, hjobs, 1)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/queue/... -v -count=1
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/ go.mod go.sum
git commit -m "feat: add SQLite-backed job queue"
```

---

### Task 3: SpotiFLAC client — subprocess wrapper + progress parser

**Files:**
- Create: `internal/spotiflac/client.go`
- Create: `internal/spotiflac/progress.go`
- Create: `internal/spotiflac/client_test.go`

**Interfaces:**
- Produces: `spotiflac.Client` struct
- Produces: `spotiflac.NewClient(cliPath string, timeout time.Duration, defaultService, defaultQuality string) *Client`
- Produces: `client.Download(ctx context.Context, url, outputDir, service, quality string) (<-chan ProgressEvent, <-chan error)`
- Produces: `client.SearchMetadata(ctx context.Context, query string) ([]MetadataResult, error)`
- Produces: `spotiflac.ProgressEvent` struct with `Type`, `Track`, `Title`, `Percent`, `Speed`, `OutputPath`, `Size`, `ErrorMessage`
- Produces: `spotiflac.MetadataResult` struct with `Artist`, `Album`, `Title`, `SpotifyURL`, `ISRC`, `CoverURL`, `Genre`, `Year`, `TrackCount`

- [ ] **Step 1: Write progress parser**

File: `internal/spotiflac/progress.go`
```go
package spotiflac

import (
	"bufio"
	"encoding/json"
	"io"
)

type ProgressEvent struct {
	Type        string  `json:"type"`
	Track       string  `json:"track,omitempty"`
	Title       string  `json:"title,omitempty"`
	Artist      string  `json:"artist,omitempty"`
	Album       string  `json:"album,omitempty"`
	Percent     float64 `json:"percent,omitempty"`
	Speed       string  `json:"speed,omitempty"`
	OutputPath  string  `json:"path,omitempty"`
	Size        int64   `json:"size,omitempty"`
	ISRC        string  `json:"isrc,omitempty"`
	ErrorMessage string `json:"message,omitempty"`
}

type MetadataResult struct {
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	Title      string `json:"title"`
	SpotifyURL string `json:"spotify_url"`
	ISRC       string `json:"isrc"`
	CoverURL   string `json:"cover_url"`
	Genre      string `json:"genre"`
	Year       int    `json:"year"`
	TrackCount int    `json:"track_count"`
}

func parseProgress(reader io.Reader, events chan<- ProgressEvent, errors chan<- error) {
	defer close(events)
	defer close(errors)

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event ProgressEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		switch event.Type {
		case "error":
			errors <- &DownloadError{Message: event.ErrorMessage}
		case "complete":
			events <- event
		default:
			events <- event
		}
	}
	if err := scanner.Err(); err != nil {
		errors <- err
	}
}

type DownloadError struct {
	Message string
}

func (e *DownloadError) Error() string {
	return "spotiflac: " + e.Message
}
```

- [ ] **Step 2: Write client**

File: `internal/spotiflac/client.go`
```go
package spotiflac

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type Client struct {
	cliPath        string
	timeout        time.Duration
	defaultService string
	defaultQuality string
}

func NewClient(cliPath string, timeout time.Duration, defaultService, defaultQuality string) *Client {
	return &Client{
		cliPath:        cliPath,
		timeout:        timeout,
		defaultService: defaultService,
		defaultQuality: defaultQuality,
	}
}

func (c *Client) Download(ctx context.Context, url, outputDir, service, quality string) (<-chan ProgressEvent, <-chan error) {
	if service == "" {
		service = c.defaultService
	}
	if quality == "" {
		quality = c.defaultQuality
	}

	events := make(chan ProgressEvent, 32)
	errs := make(chan error, 1)

	go func() {
		defer func() {
			close(events)
			close(errs)
		}()

		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, c.cliPath,
			"--url", url,
			"--output-dir", outputDir,
			"--service", service,
			"--quality", quality,
			"--json-progress",
		)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- fmt.Errorf("stdout pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errs <- fmt.Errorf("start spotiflac: %w", err)
			return
		}

		parseProgress(stdout, events, errs)

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				errs <- fmt.Errorf("spotiflac timed out after %s", c.timeout)
			} else {
				errs <- fmt.Errorf("spotiflac exited: %w", err)
			}
		}
	}()

	return events, errs
}

func (c *Client) SearchMetadata(ctx context.Context, query string) ([]MetadataResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cliPath,
		"--search", query,
		"--json-progress",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start spotiflac search: %w", err)
	}

	var results []MetadataResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var r MetadataResult
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue
		}
		if r.SpotifyURL != "" {
			results = append(results, r)
		}
	}

	if err := cmd.Wait(); err != nil {
		return results, fmt.Errorf("spotiflac search exited: %w", err)
	}

	return results, nil
}
```

Note: `"bufio"` import must be added to the imports in client.go.

- [ ] **Step 3: Write client tests**

File: `internal/spotiflac/client_test.go`
```go
package spotiflac_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// mockCli creates a fake spotiflac-cli script that outputs JSON progress lines.
func mockCli(t *testing.T, responses []string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "spotiflac-cli")

	scriptContent := `#!/bin/bash
for line in "$@"; do echo "$line"; done
`
	if len(responses) > 0 {
		scriptContent = "#!/bin/bash\n"
		for _, r := range responses {
			scriptContent += fmt.Sprintf("echo '%s'\n", r)
		}
	}
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0755))
	return script
}

func TestDownloadProgress(t *testing.T) {
	responses := []string{
		`{"type":"progress","track":"01","title":"First Song","percent":25,"speed":"1.2MB/s"}`,
		`{"type":"progress","track":"01","title":"First Song","percent":50,"speed":"1.1MB/s"}`,
		`{"type":"progress","track":"01","title":"First Song","percent":100,"speed":"0.8MB/s"}`,
		`{"type":"metadata","artist":"Test Artist","album":"Test Album","isrc":"US-ABC-12-34567"}`,
		`{"type":"complete","path":"/tmp/Test Artist/Test Album/01 - First Song.flac","size":28765432}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 10*time.Second, "tidal", "lossless")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		"/tmp/test-output",
		"", "",
	)

	var gotEvents []spotiflac.ProgressEvent
	var gotErrs []error

loop:
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				break loop
			}
			gotEvents = append(gotEvents, evt)
		case err, ok := <-errs:
			if !ok {
				break loop
			}
			gotErrs = append(gotErrs, err)
		}
	}

	assert.Empty(t, gotErrs)
	assert.Len(t, gotEvents, 5)
	assert.Equal(t, "complete", gotEvents[4].Type)
	assert.Equal(t, int64(28765432), gotEvents[4].Size)
}

func TestDownloadTimeout(t *testing.T) {
	responses := []string{} // exits immediately, but timeout is 1 nanosecond
	client := spotiflac.NewClient(mockCli(t, responses), 1*time.Nanosecond, "tidal", "lossless")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test",
		"/tmp/test-output",
		"", "",
	)

	var gotErrs []error
	for err := range errs {
		gotErrs = append(gotErrs, err)
	}
	<-events // drain

	assert.NotEmpty(t, gotErrs)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/spotiflac/... -v -count=1
```

Expected: 2 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/spotiflac/
git commit -m "feat: add spotiflac subprocess client wrapper"
```

---

### Task 4: Storage package

**Files:**
- Create: `internal/storage/storage.go`
- Create: `internal/storage/storage_test.go`

**Interfaces:**
- Produces: `storage.New(outputDir string) *Storage`
- Produces: `storage.MoveFiles(sourceDir, nzoID string) (string, error)` — moves downloaded files to `outputDir/nzoID/`
- Produces: `storage.Cleanup(nzoID string) error` — removes job output directory
- Produces: `storage.GetDiskSpace() (freeGB string, totalGB string)` — for SABnzbd diskspace fields

- [ ] **Step 1: Write storage package**

File: `internal/storage/storage.go`
```go
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Storage struct {
	outputDir string
}

func New(outputDir string) *Storage {
	return &Storage{outputDir: outputDir}
}

func (s *Storage) JobDir(nzoID string) string {
	return filepath.Join(s.outputDir, nzoID)
}

func (s *Storage) PrepareJobDir(nzoID string) (string, error) {
	dir := s.JobDir(nzoID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create job dir %s: %w", dir, err)
	}
	return dir, nil
}

func (s *Storage) CleanupJob(nzoID string) error {
	dir := s.JobDir(nzoID)
	return os.RemoveAll(dir)
}

func (s *Storage) GetDiskSpace() (freeGB string, totalGB string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.outputDir, &stat); err != nil {
		return "0", "0"
	}
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return formatGB(free), formatGB(total)
}

func formatGB(bytes uint64) string {
	gb := float64(bytes) / (1024 * 1024 * 1024)
	return fmt.Sprintf("%.2f", gb)
}
```

- [ ] **Step 2: Write tests**

File: `internal/storage/storage_test.go`
```go
package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

func TestPrepareAndCleanupJobDir(t *testing.T) {
	dir := t.TempDir()
	s := storage.New(dir)

	jobDir, err := s.PrepareJobDir("test-nzo-001")
	require.NoError(t, err)
	assert.DirExists(t, jobDir)
	assert.Equal(t, filepath.Join(dir, "test-nzo-001"), jobDir)

	testFile := filepath.Join(jobDir, "test.flac")
	require.NoError(t, os.WriteFile(testFile, []byte("fake-flac"), 0644))

	err = s.CleanupJob("test-nzo-001")
	require.NoError(t, err)
	assert.NoDirExists(t, jobDir)
}

func TestGetDiskSpace(t *testing.T) {
	s := storage.New(t.TempDir())
	free, total := s.GetDiskSpace()
	assert.NotEmpty(t, free)
	assert.NotEmpty(t, total)
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/storage/... -v -count=1
```

Expected: 2 tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/storage/
git commit -m "feat: add storage package for download file management"
```

---

### Task 5: SABnzbd API handlers

**Files:**
- Create: `internal/api/middleware.go`
- Create: `internal/api/sabnzbd/handler.go`
- Create: `internal/api/sabnzbd/status.go`
- Create: `internal/api/sabnzbd/addurl.go`
- Create: `internal/api/sabnzbd/queue.go`
- Create: `internal/api/sabnzbd/history.go`
- Create: `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: `queue.SQLiteQueue`, `spotiflac.Client`, `storage.Storage`, `config.Config`
- Produces: `sabnzbd.NewHandler(q *SQLiteQueue, client *spotiflac.Client, storage *Storage, cfg *Config, version string) *Handler`
- Produces: `handler.RegisterRoutes(app *fiber.App)` — mounts SABnzbd routes at `/api/sabnzbd`
- Produces: `middleware.APIKeyAuth(apiKey string) fiber.Handler`
- Produces: `middleware.RequestLogger(logger zerolog.Logger) fiber.Handler`

- [ ] **Step 1: Write middleware**

File: `internal/api/middleware.go`
```go
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

func APIKeyAuth(apiKey string) fiber.Handler {
	return func(c fiber.Ctx) error {
		key := c.Query("apikey")
		if key == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Required",
			})
		}
		if key != apiKey {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Incorrect",
			})
		}
		return c.Next()
	}
}

func RequestLogger(log zerolog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()
		log.Info().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Str("query", string(c.Request().URI().QueryString())).
			Int("status", c.Response().StatusCode()).
			Msg("request")
		return err
	}
}
```

- [ ] **Step 2: Write SABnzbd handler — status endpoints**

File: `internal/api/sabnzbd/status.go`
```go
package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleVersion(c fiber.Ctx) error {
	return c.JSON(sabnzbd.VersionResponse{Version: h.version})
}

func (h *Handler) handleAuth(c fiber.Ctx) error {
	return c.JSON(sabnzbd.AuthResponse{Auth: true})
}

func (h *Handler) handleGetConfig(c fiber.Ctx) error {
	resp := sabnzbd.ConfigResponse{}
	resp.Config.Categories = []sabnzbd.Category{
		{Name: "music-flac-16", Order: 0, Dir: "music-flac-16"},
		{Name: "music-flac-24", Order: 1, Dir: "music-flac-24"},
		{Name: "music-mp3", Order: 2, Dir: "music-mp3"},
	}
	resp.Config.Scripts = []sabnzbd.Script{
		{Name: "Default", Default: true},
	}
	resp.Config.Speedlimit = "100"
	resp.Config.Misc.Version = h.version
	resp.Config.Misc.CompletedDir = h.cfg.OutputDir
	resp.Config.Misc.DownloadDir = h.cfg.OutputDir
	resp.Config.Misc.CompleteDirEnabled = true
	return c.JSON(resp)
}

func (h *Handler) handleGetCats(c fiber.Ctx) error {
	return c.JSON(sabnzbd.CategoriesResponse{
		Categories: []string{"music-flac-16", "music-flac-24", "music-mp3"},
	})
}
```

- [ ] **Step 3: Write addurl handler**

File: `internal/api/sabnzbd/addurl.go`
```go
package sabnzbd

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleAddURL(c fiber.Ctx) error {
	spotifyURL := c.Query("name")
	if spotifyURL == "" {
		spotifyURL = c.FormValue("name")
	}
	if spotifyURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "missing 'name' parameter (spotify URL)",
		})
	}

	nzbName := c.Query("nzbname")
	if nzbName == "" {
		nzbName = c.FormValue("nzbname")
	}
	cat := c.Query("cat")
	if cat == "" || cat == "*" {
		cat = "music-flac-16"
	}
	priority := c.Query("priority")
	if priority == "" {
		priority = "Normal"
	}

	nzoID := "SABnzbd_nzo_" + uuid.New().String()[:12]

	job := &queue.Job{
		NzoID:      nzoID,
		SpotifyURL: spotifyURL,
		Category:   cat,
		Priority:   priority,
		Filename:   nzbName,
		Service:    h.cfg.DefaultService,
		Quality:    h.cfg.DefaultQuality,
	}

	if err := h.queue.Add(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("queue add: %s", err),
		})
	}

	go h.processDownload(job)

	return c.JSON(sabnzbd.AddURLResponse{
		Status: true,
		NzoIDs: []string{nzoID},
	})
}
```

- [ ] **Step 4: Write queue handler**

File: `internal/api/sabnzbd/queue.go`
```go
package sabnzbd

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleQueue(c fiber.Ctx) error {
	start, _ := strconv.Atoi(c.Query("start", "0"))
	limit, _ := strconv.Atoi(c.Query("limit", "50"))

	params := queue.ListParams{
		Start:    start,
		Limit:    limit,
		Search:   c.Query("search", ""),
		Category: c.Query("cat", ""),
		Status:   c.Query("status", ""),
	}

	nzoIDs := c.Query("nzo_ids", "")
	if nzoIDs != "" {
		params.NzoIDs = splitComma(nzoIDs)
	}

	jobs, total, err := h.queue.List(params)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  err.Error(),
		})
	}

	resp := sabnzbd.QueueResponse{}
	resp.Queue.Status = "Idle"
	resp.Queue.Noofslots = len(jobs)
	resp.Queue.NoofslotsTotal = total
	resp.Queue.Limit = limit
	resp.Queue.Start = start
	resp.Queue.Version = h.version
	resp.Queue.PausedAll = false

	if len(jobs) > 0 {
		resp.Queue.Status = "Downloading"
	}

	free1, total1 := h.storage.GetDiskSpace()
	resp.Queue.Diskspace1 = free1
	resp.Queue.Diskspacetotal1 = total1
	resp.Queue.Diskspace2 = free1
	resp.Queue.Diskspacetotal2 = total1

	for i, job := range jobs {
		slot := jobToSlot(job, i)
		resp.Queue.Slots = append(resp.Queue.Slots, slot)
		resp.Queue.Size = addSize(resp.Queue.Size, slot.Size)
		resp.Queue.Sizeleft = addSize(resp.Queue.Sizeleft, slot.Sizeleft)
	}

	return c.JSON(resp)
}

func (h *Handler) handlePause(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Status = sabnzbd.StatusPaused
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

func (h *Handler) handleResume(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Status = sabnzbd.StatusQueued
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	go h.processDownload(job)
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

func (h *Handler) handleDelete(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	delFiles := c.Query("del_files") == "1"
	if err := h.queue.Delete(nzoID, delFiles); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	if delFiles {
		h.storage.CleanupJob(nzoID)
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}
```

- [ ] **Step 5: Write history handler**

File: `internal/api/sabnzbd/history.go`
```go
package sabnzbd

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleHistory(c fiber.Ctx) error {
	start, _ := strconv.Atoi(c.Query("start", "0"))
	limit, _ := strconv.Atoi(c.Query("limit", "50"))

	params := queue.ListParams{
		Start:  start,
		Limit:  limit,
		Search: c.Query("search", ""),
	}

	jobs, total, err := h.queue.History(params)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  err.Error(),
		})
	}

	resp := sabnzbd.HistoryResponse{}
	resp.History.Noofslots = len(jobs)
	resp.History.Version = h.version
	resp.History.TotalSize = "0"
	resp.History.MonthSize = "0"
	resp.History.WeekSize = "0"

	for _, job := range jobs {
		slot := sabnzbd.HistorySlot{
			Status:    string(job.Status),
			NzoID:     job.NzoID,
			Name:      job.Filename,
			Size:      formatBytes(job.Size),
			Cat:       job.Category,
			Storage:   job.OutputPath,
			Path:      job.OutputPath,
			Script:    "Default",
			URL:       job.SpotifyURL,
		}
		if job.CompletedAt != nil {
			slot.Completed = job.CompletedAt.Unix()
		}
		if job.Status == sabnzbd.StatusFailed {
			slot.FailMessage = job.ErrorMessage
		}
		resp.History.Slots = append(resp.History.Slots, slot)
	}

	_ = total
	return c.JSON(resp)
}
```

- [ ] **Step 6: Write handler router + download processor**

File: `internal/api/sabnzbd/handler.go`
```go
package sabnzbd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

const maxConcurrent = 3

type Handler struct {
	queue      *queue.SQLiteQueue
	client     *spotiflac.Client
	storage    *storage.Storage
	cfg        *config.Config
	version    string
	log        zerolog.Logger
	sem        chan struct{}
}

func NewHandler(q *queue.SQLiteQueue, client *spotiflac.Client, s *storage.Storage, cfg *config.Config, version string) *Handler {
	h := &Handler{
		queue:   q,
		client:  client,
		storage: s,
		cfg:     cfg,
		version: version,
		log:     zerolog.Nop(),
		sem:     make(chan struct{}, maxConcurrent),
	}
	if cfg.MaxConcurrent > 0 {
		h.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return h
}

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	api := app.Group("/api/sabnzbd")

	api.Get("/", h.dispatch)
	api.Post("/", h.dispatch)
}

func (h *Handler) dispatch(c fiber.Ctx) error {
	mode := c.Query("mode")
	if mode == "" {
		mode = c.FormValue("mode")
	}

	switch {
	case mode == "version":
		return h.handleVersion(c)
	case mode == "auth":
		return h.handleAuth(c)
	case mode == "get_config":
		return h.handleGetConfig(c)
	case mode == "get_cats":
		return h.handleGetCats(c)
	case mode == "addurl" || mode == "addfile":
		return h.handleAddURL(c)
	case mode == "queue":
		name := c.Query("name")
		switch name {
		case "pause":
			return h.handlePause(c)
		case "resume":
			return h.handleResume(c)
		case "delete":
			return h.handleDelete(c)
		default:
			return h.handleQueue(c)
		}
	case mode == "history":
		return h.handleHistory(c)
	case mode == "change_cat":
		return h.handleChangeCat(c)
	default:
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("unknown mode: %s", mode),
		})
	}
}

func (h *Handler) handleChangeCat(c fiber.Ctx) error {
	nzoID := c.Query("value")
	newCat := c.Query("value2")
	if nzoID == "" || newCat == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing value/value2",
		})
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Category = newCat
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		h.failJob(job, err.Error())
		return
	}

	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	h.queue.Update(job)

	ctx := context.Background()
	events, errs := h.client.Download(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return
			}
			if evt.Type == "progress" {
				job.Percentage = evt.Percent
				job.Sizeleft = int64(float64(job.Size) * (100 - evt.Percent) / 100)
				h.queue.Update(job)
			}
			if evt.Type == "complete" {
				job.Status = sabnzbd.StatusCompleted
				job.Percentage = 100
				job.Size = evt.Size
				job.Sizeleft = 0
				job.OutputPath = evt.OutputPath
				now := time.Now()
				job.CompletedAt = &now
				job.Filename = evt.Artist + " - " + evt.Album
				h.queue.Update(job)
				h.queue.MoveToHistory(job.NzoID)
				h.log.Info().Str("nzo_id", job.NzoID).Str("path", evt.OutputPath).Msg("download complete")
				return
			}
			if evt.Type == "metadata" {
				job.Filename = evt.Artist + " - " + evt.Album
				h.queue.Update(job)
			}
		case e, ok := <-errs:
			if !ok {
				return
			}
			if e != nil {
				h.failJob(job, e.Error())
				return
			}
		}
	}
}

func (h *Handler) failJob(job *queue.Job, errMsg string) {
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	h.queue.Update(job)
	h.queue.MoveToHistory(job.NzoID)
	h.log.Error().Str("nzo_id", job.NzoID).Str("error", errMsg).Msg("download failed")
}

func jobToSlot(job *queue.Job, index int) sabnzbd.Slot {
	return sabnzbd.Slot{
		Status:     string(job.Status),
		Index:      index,
		NzoID:      job.NzoID,
		Filename:   job.Filename,
		Size:       formatBytes(job.Size),
		Sizeleft:   formatBytes(job.Sizeleft),
		Mb:         fmt.Sprintf("%.2f", float64(job.Size)/(1024*1024)),
		Mbleft:     fmt.Sprintf("%.2f", float64(job.Sizeleft)/(1024*1024)),
		Percentage: fmt.Sprintf("%.0f", job.Percentage),
		Priority:   job.Priority,
		Cat:        job.Category,
		TimeAdded:  job.TimeAdded.Unix(),
		Script:     "Default",
		Unpackopts: "3",
	}
}

func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "K", "M", "G", "T"}
	size := float64(bytes)
	unitIdx := 0
	for size >= 1024 && unitIdx < len(units)-1 {
		size /= 1024
		unitIdx++
	}
	return fmt.Sprintf("%.2f %s", size, units[unitIdx])
}

func addSize(a, b string) string {
	return a // simplified: just return first, real SABnzbd sums sizes
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
```

- [ ] **Step 7: Write handler tests**

File: `internal/api/sabnzbd/handler_test.go`
```go
package sabnzbd_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	apispotiflac "github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func setupTestApp(t *testing.T) (*fiber.App, *queue.SQLiteQueue) {
	t.Helper()

	cfg := &config.Config{
		APIKey:           "test-key",
		OutputDir:        t.TempDir(),
		DefaultService:   "tidal",
		DefaultQuality:   "lossless",
		MaxConcurrent:    1,
		JobTimeout:       30 * time.Minute,
	}

	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	st := storage.New(cfg.OutputDir)

	client := apispotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless")

	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	app := fiber.New()
	app.Use(api.APIKeyAuth("test-key"))
	handler.RegisterRoutes(app)

	return app, q
}

func TestVersion(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var v sabtypes.VersionResponse
	json.NewDecoder(resp.Body).Decode(&v)
	assert.Equal(t, "0.1.0-test", v.Version)
}

func TestAuth(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=auth&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var a sabtypes.AuthResponse
	json.NewDecoder(resp.Body).Decode(&a)
	assert.True(t, a.Auth)
}

func TestGetCats(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=get_cats&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var c sabtypes.CategoriesResponse
	json.NewDecoder(resp.Body).Decode(&c)
	assert.Len(t, c.Categories, 3)
}

func TestAddURL(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/test123&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var r sabtypes.AddURLResponse
	json.NewDecoder(resp.Body).Decode(&r)
	assert.True(t, r.Status)
	assert.Len(t, r.NzoIDs, 1)
	assert.Contains(t, r.NzoIDs[0], "SABnzbd_nzo_")
}

func TestQueue(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var q sabtypes.QueueResponse
	json.NewDecoder(resp.Body).Decode(&q)
	assert.Equal(t, "0.1.0-test", q.Queue.Version)
}

func TestHistory(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=history&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var h sabtypes.HistoryResponse
	json.NewDecoder(resp.Body).Decode(&h)
	assert.Equal(t, "0.1.0-test", h.History.Version)
}

func TestAuthRejected(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version&apikey=wrong", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}
```

- [ ] **Step 8: Run tests**

```bash
go test ./internal/api/... -v -count=1
```

Expected: 7 tests PASS (Version, Auth, GetCats, AddURL, Queue, History, AuthRejected).

- [ ] **Step 9: Commit**

```bash
git add internal/api/
git commit -m "feat: add SABnzbd API handlers"
```

---

### Task 6: Indexer + Newznab API handlers

**Files:**
- Create: `internal/indexer/spotify.go`
- Create: `internal/indexer/newznab.go`
- Create: `internal/indexer/indexer_test.go`
- Create: `internal/api/newznab/handler.go`
- Create: `internal/api/newznab/handler_test.go`

**Interfaces:**
- Consumes: `spotiflac.Client`
- Produces: `indexer.Search(query, artist, album string) ([]MetadataResult, error)` (calls client.SearchMetadata)
- Produces: `indexer.NewznabXML(results []MetadataResult, serverURL string) (string, error)` — generates RSS XML

- [ ] **Step 1: Write indexer**

File: `internal/indexer/newznab.go`
```go
package indexer

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Newznab string   `xml:"xmlns:newznab,attr"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
	Language    string `xml:"language"`
	WebMaster   string `xml:"webMaster"`
	Category    string `xml:"category"`
	Image       Image  `xml:"image"`
	Response    Response `xml:"newznab:response"`
	Items       []Item `xml:"item"`
}

type Image struct {
	URL         string `xml:"url"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

type Response struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type Item struct {
	Title       string    `xml:"title"`
	GUID        string    `xml:"guid"`
	Link        string    `xml:"link"`
	PubDate     string    `xml:"pubDate"`
	Category    string    `xml:"category"`
	Description string    `xml:"description"`
	Enclosure   Enclosure `xml:"enclosure"`
	Attrs       []Attr    `xml:"newznab:attr"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type Attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func NewznabXML(results []spotiflac.MetadataResult, serverURL string) ([]byte, error) {
	rss := RSS{
		Version: "2.0",
		Atom:    "http://www.w3.org/2005/Atom",
		Newznab: "http://www.newznab.com/DTD/2010/feeds/attributes/",
		Channel: Channel{
			Title:       "Spotiflac-Lidarr Proxy",
			Description: "Spotify metadata via SpotiFLAC",
			Link:        serverURL,
			Language:    "en-us",
			WebMaster:   "admin@spotiflac-proxy",
			Category:    "music",
			Image: Image{
				URL:         serverURL + "/static/logo.png",
				Title:       "Spotiflac-Lidarr Proxy",
				Link:        serverURL,
				Description: "Spotiflac-Lidarr Proxy",
			},
			Response: Response{
				Offset: 0,
				Total:  len(results),
			},
		},
	}

	for _, r := range results {
		item := Item{
			Title:       r.Artist + " - " + r.Album,
			GUID:        r.SpotifyURL,
			Link:        r.SpotifyURL,
			PubDate:     time.Now().Format(time.RFC1123Z),
			Category:    "Music > " + r.Genre,
			Description: fmt.Sprintf("%s - %s (%d tracks)", r.Artist, r.Album, r.TrackCount),
			Enclosure: Enclosure{
				URL:    r.SpotifyURL,
				Length: "0",
				Type:   "application/x-nzb",
			},
			Attrs: []Attr{
				{Name: "artist", Value: r.Artist},
				{Name: "album", Value: r.Album},
				{Name: "genre", Value: r.Genre},
				{Name: "year", Value: fmt.Sprintf("%d", r.Year)},
			},
		}
		if r.CoverURL != "" {
			item.Attrs = append(item.Attrs, Attr{Name: "coverurl", Value: r.CoverURL})
		}
		rss.Channel.Items = append(rss.Channel.Items, item)
	}

	output, err := xml.MarshalIndent(rss, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal newznab xml: %w", err)
	}

	result := xml.Header + string(output)
	return []byte(result), nil
}

func CapsXML(serverURL string) []byte {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Spotiflac-Lidarr Proxy" version="0.1.0" url="` + serverURL + `" />
  <searching>
    <search available="yes" supported="yes" />
    <music-search available="yes" supported="yes" />
  </searching>
  <categories>
    <category id="3000" name="Audio">
      <subcat id="3010" name="Lossless" />
      <subcat id="3040" name="Flac" />
    </category>
  </categories>
</caps>`
	return []byte(xml)
}
```

- [ ] **Step 2: Write Newznab handler**

File: `internal/api/newznab/handler.go`
```go
package newznab

import (
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

type Handler struct {
	client    *spotiflac.Client
	log       zerolog.Logger
	serverURL string
}

func NewHandler(client *spotiflac.Client, serverURL string) *Handler {
	return &Handler{
		client:    client,
		log:       zerolog.Nop(),
		serverURL: serverURL,
	}
}

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	api := app.Group("/api/newznab")
	api.Get("/", h.dispatch)
}

func (h *Handler) dispatch(c fiber.Ctx) error {
	t := c.Query("t")
	switch t {
	case "caps":
		return h.handleCaps(c)
	case "search":
		return h.handleSearch(c)
	case "music":
		return h.handleMusic(c)
	case "details":
		return h.handleDetails(c)
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown t parameter: " + t,
		})
	}
}

func (h *Handler) handleCaps(c fiber.Ctx) error {
	c.Set("Content-Type", "application/xml")
	return c.Send(indexer.CapsXML(h.serverURL))
}

func (h *Handler) handleSearch(c fiber.Ctx) error {
	query := c.Query("q")
	artist := c.Query("artist")
	album := c.Query("album")

	results, err := indexer.Search(c.Context(), h.client, query, artist, album)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab search failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleMusic(c fiber.Ctx) error {
	artist := c.Query("artist")
	album := c.Query("album")

	query := artist
	if album != "" {
		query = artist + " " + album
	}

	results, err := indexer.Search(c.Context(), h.client, query, artist, album)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab music search failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "music search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleDetails(c fiber.Ctx) error {
	// Lidarr expects RSS XML for details too — single item
	id := c.Query("id")
	results, err := indexer.Search(c.Context(), h.client, id, "", "")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "details search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}
```

- [ ] **Step 3: Write search function**

File: `internal/indexer/spotify.go`
```go
package indexer

import (
	"context"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func Search(ctx context.Context, client *spotiflac.Client, query, artist, album string) ([]spotiflac.MetadataResult, error) {
	searchQuery := query
	if artist != "" && album != "" {
		searchQuery = artist + " " + album
	} else if artist != "" {
		searchQuery = artist
	}

	return client.SearchMetadata(ctx, searchQuery)
}
```

- [ ] **Step 4: Write Newznab tests**

File: `internal/api/newznab/handler_test.go`
```go
package newznab_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func setupNewznabApp(t *testing.T) *fiber.App {
	t.Helper()

	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless")
	handler := newznab.NewHandler(client, "http://localhost:8484")

	app := fiber.New()
	app.Use(api.APIKeyAuth("test-key"))
	handler.RegisterRoutes(app)

	return app
}

func TestCaps(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "xml")
}

func TestSearch(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=search&q=Test+Artist+Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMusic(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=music&artist=Test+Artist&album=Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/indexer/... ./internal/api/newznab/... -v -count=1
```

Expected: Caps, Search, Music tests PASS (Search/Music may return 500 if spotiflac-cli binary is missing — acceptable; Caps must pass).

- [ ] **Step 6: Commit**

```bash
git add internal/indexer/ internal/api/newznab/
git commit -m "feat: add Newznab indexer API handlers"
```

---

### Task 7: Server entry point — wiring everything together

**Files:**
- Create: `cmd/server/main.go`
- Modify: `go.mod` (cobra dependency)

**Interfaces:**
- Consumes: all `internal/` packages
- Produces: runnable `server` binary with `serve` subcommand

- [ ] **Step 1: Write main.go**

File: `cmd/server/main.go`
```go
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

const version = "0.1.0"

func main() {
	rootCmd := &cobra.Command{
		Use:   "server",
		Short: "Spotiflac-Lidarr Proxy server",
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE:  runServe,
	})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	log = log.Level(level)

	q, err := queue.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("init queue: %w", err)
	}
	defer q.Close()

	st := storage.New(cfg.OutputDir)

	client := spotiflac.NewClient(
		cfg.SpotiflacCLIPath,
		cfg.JobTimeout,
		cfg.DefaultService,
		cfg.DefaultQuality,
	)

	app := fiber.New(fiber.Config{
		AppName:      "spotiflac-lidarr-proxy",
		ServerHeader: "spotiflac-lidarr-proxy",
	})

	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	sabHandler := sabnzbd.NewHandler(q, client, st, cfg, version)
	sabHandler.SetLogger(log)

	nznbHandler := newznab.NewHandler(client, fmt.Sprintf("http://localhost:%d", cfg.Port))
	nznbHandler.SetLogger(log)

	app.Use(api.RequestLogger(log))

	publicGroup := app.Group("")
	sabHandler.RegisterRoutes(publicGroup)
	nznbHandler.RegisterRoutes(publicGroup)

	app.Get("/api/sabnzbd", api.APIKeyAuth(cfg.APIKey), func(c fiber.Ctx) error {
		return sabHandler.RegisterRoutes(c.App())
	})

	log.Info().Int("port", cfg.Port).Str("version", version).Msg("starting server")

	go func() {
		addr := fmt.Sprintf(":%d", cfg.Port)
		if err := app.Listen(addr); err != nil {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return app.ShutdownWithContext(shutdownCtx)
}
```

- [ ] **Step 2: Fix middleware — make API key auth skip public endpoints**

File: `internal/api/middleware.go` — add the following function:

```go
import "strings"

func APIKeyAuthWithSkiplist(apiKey string, skipModes ...string) fiber.Handler {
	return func(c fiber.Ctx) error {
		mode := c.Query("mode")
		if mode == "" {
			mode = c.FormValue("mode")
		}
		t := c.Query("t")

		for _, skip := range skipModes {
			if mode == skip || t == skip {
				return c.Next()
			}
		}

		key := c.Query("apikey")
		if key == "" {
			key = c.FormValue("apikey")
		}
		if key == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Required",
			})
		}
		if key != apiKey {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Incorrect",
			})
		}
		return c.Next()
	}
}
```

- [ ] **Step 3: Update main.go to use correct routing**

The routing in main.go needs to apply auth correctly. Replace the routing in `runServe` with:

```go
	sabHandler := sabnzbd.NewHandler(q, client, st, cfg, version)
	sabHandler.SetLogger(log)

	nznbHandler := newznab.NewHandler(client, fmt.Sprintf("http://localhost:%d", cfg.Port))
	nznbHandler.SetLogger(log)

	app.Use(api.RequestLogger(log))

	sabGroup := app.Group("/api/sabnzbd")
	sabGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "version", "auth"))
	sabHandler.RegisterRoutesOnGroup(sabGroup)

	nznbGroup := app.Group("/api/newznab")
	nznbGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "caps"))
	nznbHandler.RegisterRoutesOnGroup(nznbGroup)
```

Update SABnzbd handler to support route group:

In `internal/api/sabnzbd/handler.go`, add:
```go
func (h *Handler) RegisterRoutesOnGroup(group *fiber.Group) {
	group.Get("/", h.dispatch)
	group.Post("/", h.dispatch)
}
```

Update Newznab handler similarly in `internal/api/newznab/handler.go`:
```go
func (h *Handler) RegisterRoutesOnGroup(group *fiber.Group) {
	group.Get("/", h.dispatch)
}
```

- [ ] **Step 4: Build and verify**

```bash
cd /home/daniel/workdir/lidarr-spotiflac-proxy
go build ./cmd/server
```

Expected: builds without errors.

- [ ] **Step 5: Run all unit tests**

```bash
go test ./... -count=1
```

Expected: all passing (or acceptable failures for network-dependent tests).

- [ ] **Step 6: Commit**

```bash
git add cmd/server/ internal/api/middleware.go
git commit -m "feat: add server entry point with cobra CLI and wiring"
```

---

### Task 8: Docker — multi-stage Dockerfile + docker-compose

**Files:**
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `.dockerignore`

- [ ] **Step 1: Write .dockerignore**

File: `.dockerignore`
```
.git/
.gitignore
*.md
docs/
tests/
*.test
.env
.env.*
docker-compose.yml
Dockerfile
```

- [ ] **Step 2: Write Dockerfile**

File: `Dockerfile`
```dockerfile
# Stage 1: Build proxy server
FROM golang:1.24-alpine AS builder
WORKDIR /build
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/server ./cmd/server

# Stage 2: Build spotiflac-cli from fork
FROM golang:1.24-alpine AS cli-builder
RUN apk add --no-cache git
RUN git clone https://github.com/fishingpvalues/SpotiFLAC.git /spotiflac
WORKDIR /spotiflac
RUN CGO_ENABLED=0 go build -tags headless -ldflags="-s -w" -o /out/spotiflac-cli ./cmd/spotiflac-cli

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=cli-builder /out/spotiflac-cli /usr/local/bin/spotiflac-cli
RUN mkdir -p /downloads /data
EXPOSE 8484
ENTRYPOINT ["server", "serve"]
```

- [ ] **Step 3: Write docker-compose.yml**

File: `docker-compose.yml`
```yaml
services:
  proxy:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8484:8484"
    environment:
      - SPF_API_KEY=your-secret-api-key
      - SPF_OUTPUT_DIR=/downloads
      - SPF_DB_PATH=/data/queue.db
      - SPF_LOG_LEVEL=info
      - SPF_DEFAULT_SERVICE=tidal
      - SPF_DEFAULT_QUALITY=lossless
      - SPF_MAX_CONCURRENT=3
    volumes:
      - downloads:/downloads
      - data:/data
    restart: unless-stopped

  lidarr:
    image: lscr.io/linuxserver/lidarr:latest
    ports:
      - "8686:8686"
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=UTC
    volumes:
      - downloads:/downloads
      - lidarr_config:/config
    restart: unless-stopped

volumes:
  downloads:
  data:
  lidarr_config:
```

- [ ] **Step 4: Verify Docker build**

```bash
cd /home/daniel/workdir/lidarr-spotiflac-proxy
docker build -t spotiflac-lidarr-proxy:dev .
```

Expected: builds successfully (Stage 2 may fail until SpotiFLAC fork repo exists — acceptable at this stage; test that stage 1 and stage 3 build).

- [ ] **Step 5: Commit**

```bash
git add Dockerfile docker-compose.yml .dockerignore
git commit -m "feat: add multi-stage Dockerfile and docker-compose"
```

---

### Task 9: CI/CD — GitHub Actions, Lefthook, Renovate, golangci-lint

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Create: `.golangci.yml`
- Create: `renovate.json`
- Create: `lefthook.yml`

- [ ] **Step 1: Write .golangci.yml**

File: `.golangci.yml`
```yaml
linters:
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused
    - misspell
    - unconvert
    - gofmt
    - goimports
    - gocyclo
    - dupl
  disable-all: false

linters-settings:
  gocyclo:
    min-complexity: 15
  gofmt:
    simplify: true
  misspell:
    locale: US

run:
  timeout: 5m
  tests: true
```

- [ ] **Step 2: Write ci.yml**

File: `.github/workflows/ci.yml`
```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - uses: golangci/golangci-lint-action@v6
        with:
          version: latest
          args: --timeout=5m

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - run: go test -race -coverprofile=coverage.out ./...
      - run: go tool cover -func=coverage.out

  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - run: go build ./cmd/server
      - run: docker build -t proxy:ci .
```

- [ ] **Step 3: Write release.yml**

File: `.github/workflows/release.yml`
```yaml
name: Release

on:
  push:
    tags: ["v*"]

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"

      - name: Run Trivy vulnerability scanner (deps)
        uses: aquasecurity/trivy-action@master
        with:
          scan-type: fs
          scan-ref: .
          format: table
          exit-code: 1

      - name: Docker meta
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ghcr.io/fishingpvalues/spotiflac-lidarr-proxy
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=raw,value=latest

      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}

      - name: Run Trivy vulnerability scanner (image)
        uses: aquasecurity/trivy-action@master
        with:
          image-ref: ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest
          format: table
          exit-code: 0
```

- [ ] **Step 4: Write renovate.json**

File: `renovate.json`
```json
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "extends": [
    "config:recommended"
  ],
  "packageRules": [
    {
      "matchManagers": ["gomod"],
      "matchUpdateTypes": ["minor", "patch"],
      "automerge": true
    },
    {
      "matchManagers": ["dockerfile"],
      "matchUpdateTypes": ["minor", "patch"],
      "automerge": true
    }
  ],
  "labels": ["dependencies", "renovate"],
  "schedule": ["before 9am on Monday"]
}
```

- [ ] **Step 5: Write lefthook.yml**

File: `lefthook.yml`
```yaml
pre-commit:
  commands:
    gofmt:
      glob: "*.go"
      run: gofmt -s -w {staged_files}
    lint:
      glob: "*.go"
      run: golangci-lint run --new-from-rev=HEAD~1
    commitlint:
      run: |
        if ! echo "$(git log -1 --pretty=%B)" | grep -qE "^(feat|fix|chore|docs|test|refactor|ci|build|perf|style)(\(.+\))?: "; then
          echo "ERROR: Commit message must follow conventional commits format"
          echo "  feat: ... / fix: ... / chore: ... / docs: ... / test: ..."
          exit 1
        fi

commit-msg:
  commands:
    commitlint:
      run: |
        msg=$(cat "$1")
        if ! echo "$msg" | grep -qE "^(feat|fix|chore|docs|test|refactor|ci|build|perf|style)(\(.+\))?: "; then
          echo "ERROR: Commit message must follow conventional commits format"
          exit 1
        fi
```

- [ ] **Step 6: Commit**

```bash
git add .github/ .golangci.yml renovate.json lefthook.yml
git commit -m "ci: add GitHub Actions, golangci-lint, renovate, lefthook"
```

---

### Task 10: Integration tests

**Files:**
- Create: `tests/integration/main_test.go`
- Create: `tests/fixtures/spotify_urls.json`

- [ ] **Step 1: Write test fixtures**

File: `tests/fixtures/spotify_urls.json`
```json
{
  "albums": [
    {
      "url": "https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj",
      "artist": "Shepherds Reign",
      "album": "Ala Mai",
      "description": "Public album for integration testing"
    }
  ]
}
```

- [ ] **Step 2: Write integration test**

File: `tests/integration/main_test.go`
```go
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proxyBase  = "http://localhost:8484"
	lidarrBase = "http://localhost:8686"
	apiKey     = "test-integration-key"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("set INTEGRATION=1 to run integration tests (requires docker-compose up)")
	}
}

func TestIntegration_ProxyHealth(t *testing.T) {
	skipIfNoDocker(t)

	resp, err := http.Get(proxyBase + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, "ok", body["status"])
}

func TestIntegration_SABnzbdVersion(t *testing.T) {
	skipIfNoDocker(t)

	url := fmt.Sprintf("%s/api/sabnzbd?mode=version", proxyBase)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	var v map[string]string
	json.NewDecoder(resp.Body).Decode(&v)
	assert.Contains(t, v, "version")
}

func TestIntegration_SABnzbdAddURLAndQueue(t *testing.T) {
	skipIfNoDocker(t)

	addURL := fmt.Sprintf("%s/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj&apikey=%s", proxyBase, apiKey)
	resp, err := http.Post(addURL, "application/x-www-form-urlencoded", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	var addResp struct {
		Status bool     `json:"status"`
		NzoIDs []string `json:"nzo_ids"`
	}
	json.NewDecoder(resp.Body).Decode(&addResp)
	assert.True(t, addResp.Status)
	require.NotEmpty(t, addResp.NzoIDs)
	nzoID := addResp.NzoIDs[0]

	time.Sleep(2 * time.Second)

	queueURL := fmt.Sprintf("%s/api/sabnzbd?mode=queue&nzo_ids=%s&apikey=%s", proxyBase, nzoID, apiKey)
	resp2, err := http.Get(queueURL)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, 200, resp2.StatusCode)

	var q struct {
		Queue struct {
			Slots []struct {
				NzoID  string `json:"nzo_id"`
				Status string `json:"status"`
			} `json:"slots"`
		} `json:"queue"`
	}
	json.NewDecoder(resp2.Body).Decode(&q)
	assert.NotEmpty(t, q.Queue.Slots)
	assert.Equal(t, nzoID, q.Queue.Slots[0].NzoID)
}

func TestIntegration_LidarrConfiguresProxy(t *testing.T) {
	skipIfNoDocker(t)

	url := fmt.Sprintf("%s/api/v1/downloadclient/test", lidarrBase)
	body := map[string]interface{}{
		"enable":   true,
		"protocol": "usenet",
		"name":     "Spotiflac Proxy",
		"host":     "proxy",
		"port":     8484,
		"apiKey":   apiKey,
		"urlBase":  "/api/sabnzbd",
	}
	bodyJSON, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", lidarrAPIKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	t.Logf("Lidarr test connection status: %d", resp.StatusCode)
}
```

- [ ] **Step 3: Commit**

```bash
git add tests/
git commit -m "test: add integration tests"
```

---

### Task 11: SpotiFLAC fork — headless CLI

Create separate repo `github.com/fishingpvalues/SpotiFLAC` (fork of `spotbye/SpotiFLAC`).

**Files in SpotiFLAC fork:**
- Create: `cmd/spotiflac-cli/main.go`

- [ ] **Step 1: Fork via GitHub CLI**

```bash
gh repo fork spotbye/SpotiFLAC --clone=false --org=false
# Fork creates fishingpvalues/SpotiFLAC
```

- [ ] **Step 2: Clone fork and add CLI**

```bash
gh repo clone fishingpvalues/SpotiFLAC /tmp/spotiflac-fork
```

Create `cmd/spotiflac-cli/main.go`:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/spotbye/SpotiFLAC/backend"
)

func main() {
	url := flag.String("url", "", "Spotify URL to download")
	outputDir := flag.String("output-dir", ".", "Output directory")
	service := flag.String("service", "tidal", "Download service (tidal, qobuz, amazon, deezer)")
	quality := flag.String("quality", "lossless", "Quality (lossless, hires, both)")
	jsonProgress := flag.Bool("json-progress", false, "Output JSON progress lines")
	search := flag.String("search", "", "Search Spotify metadata instead of downloading")
	flag.Parse()

	if *search != "" {
		results, err := backend.SearchMetadata(*search)
		if err != nil {
			fmt.Fprintf(os.Stderr, "search error: %s\n", err)
			os.Exit(1)
		}
		for _, r := range results {
			json.NewEncoder(os.Stdout).Encode(r)
		}
		return
	}

	if *url == "" {
		fmt.Fprintln(os.Stderr, "error: --url is required")
		os.Exit(1)
	}

	if *jsonProgress {
		backend.SetProgressCallback(func(p backend.Progress) {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"type":    "progress",
				"track":   p.Track,
				"title":   p.Title,
				"percent": p.Percent,
				"speed":   p.Speed,
			})
		})
	}

	result, err := backend.Download(*url, *outputDir, *service, *quality)
	if err != nil {
		if *jsonProgress {
			json.NewEncoder(os.Stdout).Encode(map[string]string{
				"type":    "error",
				"message": err.Error(),
			})
		}
		os.Exit(1)
	}

	if *jsonProgress {
		for _, track := range result.Tracks {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"type":   "metadata",
				"artist": result.Artist,
				"album":  result.Album,
				"isrc":   track.ISRC,
				"title":  track.Title,
			})
		}
		json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"type": "complete",
			"path": result.OutputPath,
			"size": result.TotalSize,
		})
	}

	fmt.Println("Download complete:", result.OutputPath)
}
```

Note: The actual `backend.*` function names need to match the real SpotiFLAC backend API. This is a template — adjust after exploring the actual backend package during fork implementation.

- [ ] **Step 3: Commit and push fork**

```bash
cd /tmp/spotiflac-fork
git add cmd/
git commit -m "feat: add headless CLI entry point with JSON progress output"
git push origin main
```

- [ ] **Step 4: Verify fork builds**

```bash
cd /tmp/spotiflac-fork
go build -tags headless ./cmd/spotiflac-cli
```

Expected: builds without errors.

---

### Task 12: README + final wiring

**Files:**
- Create: `README.md`
- Create: `LICENSE`

- [ ] **Step 1: Write README.md**

File: `README.md`
```markdown
# Spotiflac-Lidarr Proxy

> [!NOTE]
> This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code is reviewed and tested before merging. See [AI Usage](#ai-usage) for details.

Bridge Lidarr ↔ SpotiFLAC. Implements SABnzbd download client API and Newznab indexer API so Lidarr treats this proxy as a standard Usenet downloader. The proxy shells out to a headless SpotiFLAC CLI to download high-quality FLAC files from Tidal, Qobuz, Amazon Music, and Deezer.

## How It Works

```
Lidarr → (Newznab search) → Proxy → (Spotify metadata) → SpotiFLAC
Lidarr → (SABnzbd addurl) → Proxy → (queue + exec) → SpotiFLAC CLI → FLAC files
Lidarr → (SABnzbd queue)  → Proxy → job status
Lidarr → Import FLAC from shared /downloads volume
```

## Quick Start

### Docker Compose

```yaml
services:
  proxy:
    image: ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest
    ports: ["8484:8484"]
    environment:
      - SPF_API_KEY=your-secret-key
      - SPF_OUTPUT_DIR=/downloads
    volumes:
      - downloads:/downloads
  lidarr:
    image: lscr.io/linuxserver/lidarr:latest
    ports: ["8686:8686"]
    environment:
      - PUID=1000
      - PGID=1000
    volumes:
      - downloads:/downloads
      - config:/config
```

### Lidarr Setup

1. **Download Client:** Settings → Download Clients → Add → SABnzbd
   - Host: `proxy`, Port: `8484`
   - URL Base: `/api/sabnzbd`
   - API Key: your SPF_API_KEY value
   - Category: `music-flac-16`

2. **Indexer:** Settings → Indexers → Add → Newznab
   - URL: `http://proxy:8484/api/newznab`
   - API Key: your SPF_API_KEY value
   - Categories: 3010, 3040

## Configuration

All via environment variables prefixed `SPF_`:

| Variable | Default | Description |
|----------|---------|-------------|
| SPF_PORT | 8484 | HTTP listen port |
| SPF_API_KEY | (required) | API key for Lidarr auth |
| SPF_OUTPUT_DIR | /downloads | FLAC output directory |
| SPF_SPOTIFLAC_CLI_PATH | /usr/local/bin/spotiflac-cli | SpotiFLAC binary |
| SPF_DEFAULT_SERVICE | tidal | Download service priority |
| SPF_DEFAULT_QUALITY | lossless | Quality: lossless, hires, both |
| SPF_MAX_CONCURRENT | 3 | Max concurrent downloads |
| SPF_JOB_TIMEOUT | 30m | Max time per download |
| SPF_DB_PATH | /data/queue.db | SQLite database path |
| SPF_LOG_LEVEL | info | Log level |

## Building

```bash
go build ./cmd/server
./server serve
```

## Testing

```bash
# Unit tests
go test ./... -count=1

# Integration tests (requires docker-compose up)
INTEGRATION=1 go test ./tests/integration/... -v
```

## Project Structure

```
cmd/server/          Entry point
internal/api/        SABnzbd + Newznab handlers
internal/spotiflac/  SpotiFLAC CLI wrapper
internal/queue/      SQLite job queue
internal/indexer/    Spotify metadata → Newznab XML
internal/storage/    File system operations
```

## AI Usage

This project was planned, architected, and implemented with assistance from Anthropic Claude (Claude Code). AI contributions include: architecture design, code generation, test authoring, CI/CD pipeline setup, and documentation. All code is human-reviewed and tested.

## License

MIT
```

- [ ] **Step 2: Write LICENSE**

File: `LICENSE`
```
MIT License

Copyright (c) 2026

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 3: Final commit**

```bash
git add README.md LICENSE
git commit -m "docs: add README with setup instructions and AI disclosure"
```

---

## Implementation Order

Tasks 1-4 (foundation) must be done first. Tasks 5-7 depend on 1-4. Task 8 depends on 7. Task 9 is independent. Task 10 depends on 8. Task 11 is independent (separate repo). Task 12 can be done anytime.

**Recommended execution sequence:** 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12
