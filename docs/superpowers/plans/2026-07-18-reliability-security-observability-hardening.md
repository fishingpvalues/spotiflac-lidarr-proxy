# Reliability, Security, Observability & Matching Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the correctness/reliability, security, observability, and Lidarr-matching gaps identified in `docs/superpowers/specs/2026-07-18-reliability-security-observability-hardening-design.md` without changing the external SABnzbd/Newznab contract except additively.

**Architecture:** All changes are internal to the existing Go packages (`internal/spotiflac`, `internal/api/sabnzbd`, `internal/queue`, `internal/storage`, `internal/indexer`, `internal/config`, `internal/api`, `cmd/server`). No new services, no new packages except `internal/breaker` (circuit breaker) and `internal/metrics` (Prometheus registration).

**Tech Stack:** Go 1.25, fiber/v3, modernc.org/sqlite, zerolog, testify. New dependency: `github.com/prometheus/client_golang` (Phase 3 only).

## Global Constraints

- Go version: 1.25+ (per `go.mod`)
- Commits: Conventional commits (`feat:`, `fix:`, `chore:`, `docs:`, `test:`) — one commit per task, per `AGENTS.md`
- Tests: table-driven, `testify/assert` + `testify/require`, test files in same package with `_test` suffix
- HTTP: fiber/v3, handlers via struct methods, no global state
- Error handling: wrap with `fmt.Errorf("context: %w", err)`, never panic in handlers
- Config: all new settings via env vars prefixed `SPF_`, defaults set in `internal/config/config.go`'s `setDefaults`
- Logging: zerolog, structured, passed via `SetLogger()`
- No changes to existing JSON/XML field names in `pkg/sabnzbd/types.go` or `internal/indexer/newznab.go` structs — only additive fields/attrs

---

## Phase 1 — Correctness + Reliability

### Task 1: Fix artist self-assignment bug in SpotiFLAC search parsing

**Files:**
- Modify: `internal/spotiflac/client.go:125-132`
- Test: `internal/spotiflac/client_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: no interface change — internal bugfix only, `SearchMetadata(ctx, query) ([]MetadataResult, error)` signature unchanged

- [ ] **Step 1: Write the failing test**

Add to `internal/spotiflac/client_test.go`:

```go
func TestSearchMetadataArtistFallsBackToName(t *testing.T) {
	responses := []string{
		`{"type":"result","name":"Fallback Name","artist":"","album":"Some Album","spotify_url":"https://open.spotify.com/album/xyz","title":"Some Album"}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 10*time.Second, "tidal", "lossless")

	results, err := client.SearchMetadata(context.Background(), "query")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Fallback Name", results[0].Artist)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/spotiflac/... -run TestSearchMetadataArtistFallsBackToName -v`
Expected: FAIL — `Fallback Name` != `""` (artist stays empty)

- [ ] **Step 3: Fix the bug**

In `internal/spotiflac/client.go`, find:

```go
		artist := raw.Artist
		if artist == "" {
			artist = raw.Artist
		}
```

Replace with:

```go
		artist := raw.Artist
		if artist == "" {
			artist = raw.Name
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/spotiflac/... -run TestSearchMetadataArtistFallsBackToName -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/spotiflac/client.go internal/spotiflac/client_test.go
git commit -m "fix: fall back to CLI name field when artist is empty in search results"
```

---

### Task 2: Consolidate category parsing, delete dead/buggy ParseCategory

**Files:**
- Modify: `internal/config/config.go` (delete `ParseCategory`, add `ParseCategory` as the correct promoted version)
- Modify: `internal/api/sabnzbd/addurl.go` (remove local `parseCategory`, call `config.ParseCategory`)
- Test: `internal/config/config_test.go` (new file — package currently has zero tests)

**Interfaces:**
- Consumes: nothing new
- Produces: `config.ParseCategory(cat string) (service, quality string)` — used by Task 3 (`handleChangeCat` fix) and by `addurl.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

func TestParseCategory(t *testing.T) {
	cases := []struct {
		cat         string
		wantService string
		wantQuality string
	}{
		{"music-tidal", "tidal", ""},
		{"music-qobuz-flac-24", "qobuz", "hires"},
		{"music-flac-16", "", "lossless"},
		{"music-amazon-flac-24", "amazon", "hires"},
		{"MUSIC-TIDAL-FLAC-16", "tidal", "lossless"},
	}
	for _, c := range cases {
		svc, qual := config.ParseCategory(c.cat)
		assert.Equal(t, c.wantService, svc, "service for %s", c.cat)
		assert.Equal(t, c.wantQuality, qual, "quality for %s", c.cat)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestParseCategory -v`
Expected: FAIL — current `ParseCategory` doesn't lowercase, so `MUSIC-TIDAL-FLAC-16` case returns empty service/quality

- [ ] **Step 3: Replace the buggy function**

In `internal/config/config.go`, replace the entire existing `ParseCategory` function:

```go
// ParseCategory extracts service and quality from a SABnzbd category name.
// Categories follow the pattern: music-[service][-quality]
// Examples: music-tidal, music-flac-16, music-qobuz-flac-24
func ParseCategory(cat string) (service, quality string) {
	cat = cat // keep lowercase
	...
}
```

with:

```go
// ParseCategory extracts service and quality from a SABnzbd category name.
// Categories follow the pattern: music-[service][-quality]
// Examples: music-tidal, music-flac-16, music-qobuz-flac-24
func ParseCategory(cat string) (service, quality string) {
	catLower := strings.ToLower(cat)

	for _, svc := range []string{ServiceTidal, ServiceQobuz, ServiceAmazon, ServiceDeezer} {
		if strings.Contains(catLower, svc) {
			service = svc
			break
		}
	}

	if strings.Contains(catLower, "flac-24") || strings.Contains(catLower, "hires") || strings.Contains(catLower, "24-bit") {
		quality = "hires"
	} else if strings.Contains(catLower, "flac-16") || strings.Contains(catLower, "lossless") || strings.Contains(catLower, "16-bit") {
		quality = "lossless"
	} else if strings.Contains(catLower, "mp3") {
		quality = "lossless"
	}

	return
}
```

- [ ] **Step 4: Remove the duplicate parser from addurl.go and call config.ParseCategory instead**

In `internal/api/sabnzbd/addurl.go`, delete the entire local `parseCategory` function (lines 75-104) and its doc comment. Change the call site:

```go
	// Extract service and quality from category
	svc, qual := parseCategory(cat)
```

to:

```go
	// Extract service and quality from category
	svc, qual := config.ParseCategory(cat)
```

Add the import in `internal/api/sabnzbd/addurl.go`:

```go
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
```

- [ ] **Step 5: Run full test suite to verify no regressions**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS (note: `internal/api/sabnzbd` no longer exports `parseCategory` — confirm no other file in that package referenced it via `grep -rn "parseCategory" internal/`)

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/api/sabnzbd/addurl.go
git commit -m "fix: consolidate category parsing into config package, fix case-sensitivity bug"
```

---

### Task 3: Fix `handleChangeCat` to re-derive service/quality

**Files:**
- Modify: `internal/api/sabnzbd/handler.go:117-138`
- Test: `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: `config.ParseCategory(cat string) (service, quality string)` from Task 2
- Produces: no interface change

- [ ] **Step 1: Write the failing test**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestChangeCatUpdatesServiceAndQuality(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_changecat",
		SpotifyURL: "https://open.spotify.com/album/test",
		Category:   "music-flac-16",
		Service:    "tidal",
		Quality:    "lossless",
	}
	require.NoError(t, q.Add(job))

	req, _ := http.NewRequest("GET",
		"/api/sabnzbd?mode=change_cat&value=SABnzbd_nzo_changecat&value2=music-qobuz-flac-24&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	got, err := q.Get("SABnzbd_nzo_changecat")
	require.NoError(t, err)
	assert.Equal(t, "music-qobuz-flac-24", got.Category)
	assert.Equal(t, "qobuz", got.Service)
	assert.Equal(t, "hires", got.Quality)
}
```

This test needs the `queue` import already present in `handler_test.go` (it is, per existing file).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestChangeCatUpdatesServiceAndQuality -v`
Expected: FAIL — `got.Service` is still `"tidal"`, not `"qobuz"`

- [ ] **Step 3: Fix `handleChangeCat`**

In `internal/api/sabnzbd/handler.go`, replace:

```go
	job.Category = newCat
	if err := h.queue.Update(job); err != nil {
```

with:

```go
	job.Category = newCat
	svc, qual := config.ParseCategory(newCat)
	if svc != "" {
		job.Service = svc
	}
	if qual != "" {
		job.Quality = qual
	}
	if err := h.queue.Update(job); err != nil {
```

Add the import to `internal/api/sabnzbd/handler.go`:

```go
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
```

(It's likely already imported for `*config.Config` — check the existing import block; if `config` is already imported, skip this step.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestChangeCatUpdatesServiceAndQuality -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/sabnzbd/handler.go internal/api/sabnzbd/handler_test.go
git commit -m "fix: re-derive service/quality on change_cat instead of only updating category label"
```

---

### Task 4: Add audio file counting helper to storage

**Files:**
- Modify: `internal/storage/storage.go`
- Test: `internal/storage/storage_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `storage.CountAudioFiles(dir string) (int, error)` — used by Task 5 (completion verification)

- [ ] **Step 1: Write the failing test**

Add to `internal/storage/storage_test.go`:

```go
func TestCountAudioFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "01.flac"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "02.flac"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("x"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "Disc 2"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Disc 2", "03.flac"), []byte("x"), 0644))

	count, err := storage.CountAudioFiles(dir)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/... -run TestCountAudioFiles -v`
Expected: FAIL — `storage.CountAudioFiles` undefined

- [ ] **Step 3: Implement `CountAudioFiles`**

Add to `internal/storage/storage.go`:

```go
var audioExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".ogg":  true,
	".opus": true,
}

// CountAudioFiles walks dir recursively (to cover multi-disc subfolders)
// and returns the number of files with a recognized audio extension.
func (s *Storage) countAudioFiles(dir string) (int, error) {
	return CountAudioFiles(dir)
}

// CountAudioFiles walks dir recursively (to cover multi-disc subfolders)
// and returns the number of files with a recognized audio extension.
func CountAudioFiles(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if audioExtensions[strings.ToLower(filepath.Ext(path))] {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("count audio files in %s: %w", dir, err)
	}
	return count, nil
}
```

Add `"io/fs"` is not needed since using `os.DirEntry` from `filepath.WalkDir` (Go 1.16+, stdlib `path/filepath`). Ensure the import block in `internal/storage/storage.go` includes `"path/filepath"` (already present) — no new imports needed beyond what's already there (`os`, `path/filepath`, `strings`, `fmt` all already imported).

Note: the unused `countAudioFiles` method wrapper above is unnecessary — remove it, keep only the package-level `CountAudioFiles` function. Final version:

```go
var audioExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".ogg":  true,
	".opus": true,
}

// CountAudioFiles walks dir recursively (to cover multi-disc subfolders)
// and returns the number of files with a recognized audio extension.
func CountAudioFiles(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if audioExtensions[strings.ToLower(filepath.Ext(path))] {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("count audio files in %s: %w", dir, err)
	}
	return count, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/... -run TestCountAudioFiles -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/storage/storage.go internal/storage/storage_test.go
git commit -m "feat: add CountAudioFiles helper for completion verification"
```

---

### Task 5: Completion verification in processDownload

**Files:**
- Modify: `internal/queue/job.go` (add `TrackCount` field)
- Modify: `internal/spotiflac/client.go` (`MetadataResult` already has `TrackCount` — no change needed there; add `TrackCount` to what `Download` needs to know, passed in by caller)
- Modify: `internal/api/sabnzbd/addurl.go` (populate `job.TrackCount` from a metadata lookup before starting the download)
- Modify: `internal/api/sabnzbd/handler.go` (`processDownload`: track whether a `complete` event was seen; verify file count before marking `Completed`)
- Test: `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: `storage.CountAudioFiles(dir string) (int, error)` from Task 4
- Produces: `queue.Job.TrackCount int` field — used by no later task, terminal

- [ ] **Step 1: Add `TrackCount` to the `Job` struct and DB schema**

In `internal/queue/job.go`, add the field:

```go
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
	TrackCount   int                `json:"track_count"`
}
```

In `internal/queue/queue.go`, add the column to the `migrate` schema:

```go
			service TEXT NOT NULL DEFAULT '',
			quality TEXT NOT NULL DEFAULT '',
			track_count INTEGER NOT NULL DEFAULT 0,
			is_history INTEGER NOT NULL DEFAULT 0
```

Update `Add`:

```go
	_, err := q.db.Exec(
		`INSERT INTO jobs (nzo_id, spotify_url, status, category, priority, filename, output_path, size, sizeleft, percentage, time_added, service, quality, track_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.NzoID, job.SpotifyURL, job.Status, job.Category, job.Priority,
		job.Filename, job.OutputPath, job.Size, job.Sizeleft, job.Percentage,
		job.TimeAdded, job.Service, job.Quality, job.TrackCount,
	)
```

Update `Get`'s SELECT/Scan, `List`'s SELECT/Scan, `Update`, and `History`'s SELECT/Scan to include `track_count` symmetrically with the existing `service`/`quality` columns (same pattern — add `track_count` to the column list in every `SELECT`, add `&job.TrackCount` to every `Scan`, add `track_count=?` + `job.TrackCount` to `Update`'s `SET` clause and args).

- [ ] **Step 2: Write the failing test for completion verification**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestProcessDownloadFailsOnTrackCountMismatch(t *testing.T) {
	outputDir := t.TempDir()
	cfg := &config.Config{
		APIKey:         "test-key",
		OutputDir:      outputDir,
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  1,
		JobTimeout:     30 * time.Second,
	}
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	st := storage.New(outputDir)

	// mockCli emits "complete" but writes only 1 file for a job expecting 2 tracks
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless")

	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_mismatch",
		SpotifyURL: "https://open.spotify.com/album/test",
		Service:    "tidal",
		Quality:    "lossless",
		TrackCount: 2,
	}
	require.NoError(t, q.Add(job))

	handler.ProcessDownloadSync(job)

	got, err := q.Get("SABnzbd_nzo_mismatch")
	// job moved to history on failure, so Get (active queue only) errors
	require.Error(t, err)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabnzbd.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "partial album")
	_ = got
}
```

This test requires `processDownload` to be callable synchronously in tests. Add a small exported wrapper (see Step 4).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadFailsOnTrackCountMismatch -v`
Expected: FAIL — `handler.ProcessDownloadSync` undefined (compile error)

- [ ] **Step 4: Implement completion verification**

In `internal/api/sabnzbd/handler.go`, add an exported synchronous wrapper right above `processDownload` (used by tests and internally by `go h.ProcessDownloadSync(job)` call sites, replacing the old `go h.processDownload(job)` pattern one-for-one):

```go
// ProcessDownloadSync runs the download synchronously. Production call sites
// wrap it in `go h.ProcessDownloadSync(job)`; tests call it directly.
func (h *Handler) ProcessDownloadSync(job *queue.Job) {
	h.processDownload(job)
}
```

Replace every `go h.processDownload(job)` call site (in `addurl.go`, `status.go`'s `handleRetry`, `queue.go`'s `handleResume`, `batch.go`'s `handleResumeAll`) with `go h.ProcessDownloadSync(job)`.

Now rewrite `processDownload`'s completion handling. Replace:

```go
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
```

with:

```go
			if evt.Type == "complete" {
				sawComplete = true
				if job.TrackCount > 0 {
					gotCount, cerr := storage.CountAudioFiles(evt.OutputPath)
					if cerr != nil || gotCount < job.TrackCount {
						h.failJob(job, fmt.Sprintf("partial album: %d/%d tracks", gotCount, job.TrackCount))
						return
					}
				}
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
```

Declare `sawComplete` at the top of `processDownload` (right after `events, errs := h.client.Download(...)`):

```go
	sawComplete := false
```

Handle the exit-0-without-complete-event case: where the `events` channel closes (`case evt, ok := <-events: if !ok { ... }`), currently it's `if !ok { return }`. Change to:

```go
			case evt, ok := <-events:
				if !ok {
					if !sawComplete {
						h.failJob(job, "cli exited without completion signal")
					}
					return
				}
```

Add `"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"` to the import block of `internal/api/sabnzbd/handler.go` — it's already imported (the `Handler` struct already has `storage *storage.Storage`), so no new import needed; just call the package-level `storage.CountAudioFiles`.

- [ ] **Step 5: Populate `TrackCount` at job creation time**

In `internal/api/sabnzbd/addurl.go`, after building the `job` struct and before `h.queue.Add(job)`, add a best-effort metadata lookup. Since `SearchMetadata` takes a free-text query (not a URL) and the CLI's search-by-URL behavior isn't separately exposed in this client, and adding a new CLI flag is out of scope for this hardening pass, use a conservative fallback: leave `TrackCount` at 0 (verification skipped) unless a future task wires a proper per-URL metadata lookup. Document this explicitly as a known limitation rather than faking a lookup:

```go
	job := &queue.Job{
		NzoID:      nzoID,
		SpotifyURL: spotifyURL,
		Category:   cat,
		Priority:   priority,
		Filename:   nzbName,
		Service:    svc,
		Quality:    qual,
		// TrackCount left at 0: the CLI's --search flag takes free-text
		// queries, not a Spotify URL, so no reliable per-URL track count
		// is available at addurl time. Completion verification in
		// processDownload only runs when TrackCount > 0.
	}
```

This means the test in Step 2 (which sets `TrackCount: 2` directly on the job before calling `q.Add`) exercises the verification logic correctly, while real `addurl`-created jobs skip the check until a proper URL-based metadata lookup exists (tracked as a follow-up, not part of this plan).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadFailsOnTrackCountMismatch -v`
Expected: PASS

- [ ] **Step 7: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/queue/job.go internal/queue/queue.go internal/api/sabnzbd/handler.go internal/api/sabnzbd/addurl.go internal/api/sabnzbd/status.go internal/api/sabnzbd/queue.go internal/api/sabnzbd/batch.go internal/api/sabnzbd/handler_test.go
git commit -m "feat: verify completion event and track count before marking jobs Completed"
```

---

### Task 6: Duplicate job rejection on addurl

**Files:**
- Modify: `internal/queue/queue.go` (add `FindActiveBySpotifyURL`, add index)
- Modify: `internal/api/sabnzbd/addurl.go` (check before creating)
- Test: `internal/queue/queue_test.go`, `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `queue.SQLiteQueue.FindActiveBySpotifyURL(url string) (*Job, error)` — returns `sql.ErrNoRows` wrapped error if none found; used only within this task

- [ ] **Step 1: Write the failing test**

Add to `internal/queue/queue_test.go`:

```go
func TestFindActiveBySpotifyURL(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_dup001",
		SpotifyURL: "https://open.spotify.com/album/dup",
		Status:     sabnzbd.StatusQueued,
	}
	require.NoError(t, q.Add(job))

	found, err := q.FindActiveBySpotifyURL("https://open.spotify.com/album/dup")
	require.NoError(t, err)
	assert.Equal(t, "SABnzbd_nzo_dup001", found.NzoID)

	_, err = q.FindActiveBySpotifyURL("https://open.spotify.com/album/nonexistent")
	assert.Error(t, err)
}

func TestFindActiveBySpotifyURLIgnoresHistory(t *testing.T) {
	q := newTestQueue(t)

	job := &queue.Job{NzoID: "SABnzbd_nzo_dup002", SpotifyURL: "https://open.spotify.com/album/dup2"}
	require.NoError(t, q.Add(job))
	require.NoError(t, q.MoveToHistory("SABnzbd_nzo_dup002"))

	_, err := q.FindActiveBySpotifyURL("https://open.spotify.com/album/dup2")
	assert.Error(t, err, "a job already moved to history should not count as a duplicate")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/queue/... -run TestFindActiveBySpotifyURL -v`
Expected: FAIL — `q.FindActiveBySpotifyURL` undefined

- [ ] **Step 3: Implement `FindActiveBySpotifyURL`**

Add to `internal/queue/queue.go`:

```go
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
```

Add an index to the `migrate` schema (right after the `CREATE TABLE` statement, still inside the same string or as a second `db.Exec`):

```go
func migrate(db *sql.DB) error {
	query := `
		CREATE TABLE IF NOT EXISTS jobs (
			...
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_spotify_url ON jobs(spotify_url, is_history, status);
		`
	_, err := db.Exec(query)
	return err
}
```

(`modernc.org/sqlite`'s `Exec` accepts multi-statement strings separated by `;` — consistent with how the existing single `CREATE TABLE` statement is already run.)

- [ ] **Step 4: Run queue tests to verify they pass**

Run: `go test ./internal/queue/... -v`
Expected: PASS

- [ ] **Step 5: Write the failing handler-level test**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestAddURLDedupReturnsExistingNzoID(t *testing.T) {
	app, _ := setupTestApp(t)

	req1, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/duptest&apikey=test-key", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	var r1 sabtypes.AddURLResponse
	json.NewDecoder(resp1.Body).Decode(&r1)

	req2, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/duptest&apikey=test-key", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	var r2 sabtypes.AddURLResponse
	json.NewDecoder(resp2.Body).Decode(&r2)

	assert.Equal(t, r1.NzoIDs[0], r2.NzoIDs[0], "re-adding the same URL should return the same nzo_id, not create a new job")
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestAddURLDedupReturnsExistingNzoID -v`
Expected: FAIL — two different `nzo_id`s returned

- [ ] **Step 7: Implement the dedup check in `handleAddURL`**

In `internal/api/sabnzbd/addurl.go`, right before `nzoID := "SABnzbd_nzo_" + uuid.New().String()[:12]`, add:

```go
	if existing, err := h.queue.FindActiveBySpotifyURL(spotifyURL); err == nil {
		return c.JSON(sabnzbd.AddURLResponse{
			Status: true,
			NzoIDs: []string{existing.NzoID},
		})
	}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestAddURLDedupReturnsExistingNzoID -v`
Expected: PASS

- [ ] **Step 9: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/queue/queue.go internal/queue/queue_test.go internal/api/sabnzbd/addurl.go internal/api/sabnzbd/handler_test.go
git commit -m "feat: reject duplicate addurl for a Spotify URL with an active job"
```

---

### Task 7: Startup crash recovery sweep

**Files:**
- Modify: `internal/queue/queue.go` (add `RecoverStuckJobs` method, call from `New`)
- Test: `internal/queue/queue_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `queue.SQLiteQueue.RecoverStuckJobs() (int, error)` — returns count of recovered jobs; called once by `New`, no other consumer

- [ ] **Step 1: Write the failing test**

Add to `internal/queue/queue_test.go`:

```go
func TestRecoverStuckJobsOnStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	q1, err := queue.New(dbPath)
	require.NoError(t, err)
	job := &queue.Job{NzoID: "SABnzbd_nzo_stuck001", Status: sabnzbd.StatusQueued}
	require.NoError(t, q1.Add(job))
	job.Status = sabnzbd.StatusDownloading
	require.NoError(t, q1.Update(job))
	require.NoError(t, q1.Close())

	// Simulate restart: reopening the DB via New() must recover the stuck job.
	q2, err := queue.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { q2.Close() })

	_, err = q2.Get("SABnzbd_nzo_stuck001")
	assert.Error(t, err, "recovered job should have moved to history, not stayed in the active queue")

	hist, _, err := q2.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabnzbd.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "interrupted by restart")
}
```

Add `"path/filepath"` to the test file's imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/queue/... -run TestRecoverStuckJobsOnStartup -v`
Expected: FAIL — job still shows as `Downloading` in the active queue after reopening

- [ ] **Step 3: Implement recovery sweep**

Add to `internal/queue/queue.go`:

```go
// RecoverStuckJobs marks any job left in Downloading status (from a prior
// crash or unclean restart) as Failed and moves it to history. Called once
// at startup — partial on-disk state from a killed subprocess is never
// trusted or auto-resumed.
func (q *SQLiteQueue) RecoverStuckJobs() (int, error) {
	rows, err := q.db.Query(`SELECT nzo_id FROM jobs WHERE status = ? AND is_history = 0`, sabnzbd.StatusDownloading)
	if err != nil {
		return 0, fmt.Errorf("query stuck jobs: %w", err)
	}
	var nzoIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan stuck job: %w", err)
		}
		nzoIDs = append(nzoIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate stuck jobs: %w", err)
	}

	for _, id := range nzoIDs {
		if _, err := q.db.Exec(
			`UPDATE jobs SET status = ?, error_message = ? WHERE nzo_id = ?`,
			sabnzbd.StatusFailed, "interrupted by restart", id,
		); err != nil {
			return 0, fmt.Errorf("mark stuck job %s failed: %w", id, err)
		}
		if err := q.MoveToHistory(id); err != nil {
			return 0, fmt.Errorf("move stuck job %s to history: %w", id, err)
		}
	}
	return len(nzoIDs), nil
}
```

Call it from `New`, right after `migrate`:

```go
func New(dbPath string) (*SQLiteQueue, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/queue/... -run TestRecoverStuckJobsOnStartup -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/queue/queue.go internal/queue/queue_test.go
git commit -m "feat: recover jobs stuck Downloading after an unclean restart"
```

---

### Task 8: Circuit breaker per service

**Files:**
- Create: `internal/breaker/breaker.go`
- Create: `internal/breaker/breaker_test.go`
- Modify: `internal/api/sabnzbd/handler.go` (wire breaker into `processDownload`)

**Interfaces:**
- Consumes: nothing new
- Produces: `breaker.Breaker` type with `Allow(service string) bool`, `RecordSuccess(service string)`, `RecordFailure(service string)`, `Status() map[string]breaker.State` — consumed by Task 9 (fallback) and Task 19 (warnings endpoint)

- [ ] **Step 1: Write the failing test**

Create `internal/breaker/breaker_test.go`:

```go
package breaker_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
)

func TestBreakerTripsAfterThreshold(t *testing.T) {
	b := breaker.New(3, 50*time.Millisecond)

	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")

	assert.False(t, b.Allow("tidal"), "breaker should be open after 3 consecutive failures")
	assert.True(t, b.Allow("qobuz"), "breaker state is per-service")
}

func TestBreakerResetsOnSuccess(t *testing.T) {
	b := breaker.New(3, time.Second)
	b.RecordFailure("tidal")
	b.RecordFailure("tidal")
	b.RecordSuccess("tidal")
	b.RecordFailure("tidal")
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"), "a success should reset the consecutive-failure count")
}

func TestBreakerClosesAfterCooldown(t *testing.T) {
	b := breaker.New(1, 20*time.Millisecond)
	b.RecordFailure("tidal")
	assert.False(t, b.Allow("tidal"))

	time.Sleep(30 * time.Millisecond)
	assert.True(t, b.Allow("tidal"), "breaker should close again after the cooldown elapses")
}

func TestBreakerStatus(t *testing.T) {
	b := breaker.New(1, time.Minute)
	b.RecordFailure("tidal")
	status := b.Status()
	assert.True(t, status["tidal"].Open)
	assert.False(t, status["qobuz"].Open)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/breaker/... -v`
Expected: FAIL — package `internal/breaker` doesn't exist

- [ ] **Step 3: Implement the breaker**

Create `internal/breaker/breaker.go`:

```go
// Package breaker implements a simple per-key consecutive-failure circuit
// breaker, in-memory only (process lifetime, no persistence).
package breaker

import (
	"sync"
	"time"
)

// State is the externally-visible status of one key's breaker.
type State struct {
	Open           bool
	ConsecutiveFailures int
	OpenedAt       time.Time
	RetryAt        time.Time
}

type entry struct {
	consecutiveFailures int
	openedAt            time.Time
}

// Breaker trips for a key after `threshold` consecutive failures and stays
// open for `cooldown` before allowing traffic again.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	entries   map[string]*entry
}

func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		entries:   make(map[string]*entry),
	}
}

// Allow reports whether a new attempt for key should proceed.
func (b *Breaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok || e.consecutiveFailures < b.threshold {
		return true
	}
	if time.Since(e.openedAt) >= b.cooldown {
		// Cooldown elapsed: allow a trial attempt, reset failure count
		// optimistically. A subsequent RecordFailure will re-open it.
		e.consecutiveFailures = 0
		return true
	}
	return false
}

func (b *Breaker) RecordFailure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok {
		e = &entry{}
		b.entries[key] = e
	}
	e.consecutiveFailures++
	if e.consecutiveFailures == b.threshold {
		e.openedAt = time.Now()
	}
}

func (b *Breaker) RecordSuccess(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if e, ok := b.entries[key]; ok {
		e.consecutiveFailures = 0
	}
}

// Status returns a snapshot of every key's breaker state seen so far.
func (b *Breaker) Status() map[string]State {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[string]State, len(b.entries))
	for key, e := range b.entries {
		open := e.consecutiveFailures >= b.threshold && time.Since(e.openedAt) < b.cooldown
		out[key] = State{
			Open:                open,
			ConsecutiveFailures: e.consecutiveFailures,
			OpenedAt:            e.openedAt,
			RetryAt:             e.openedAt.Add(b.cooldown),
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/breaker/... -v`
Expected: PASS

- [ ] **Step 5: Wire the breaker into `Handler`**

In `internal/api/sabnzbd/handler.go`, add a field and constructor wiring:

```go
type Handler struct {
	queue   *queue.SQLiteQueue
	client  *spotiflac.Client
	storage *storage.Storage
	cfg     *config.Config
	version string
	log     zerolog.Logger
	sem     chan struct{}
	breaker *breaker.Breaker
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
		breaker: breaker.New(5, 10*time.Minute),
	}
	if cfg.MaxConcurrent > 0 {
		h.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return h
}
```

Add import `"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"` to `internal/api/sabnzbd/handler.go`.

At the top of `processDownload`, before spawning the CLI, check the breaker:

```go
func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	if !h.breaker.Allow(job.Service) {
		h.failJob(job, fmt.Sprintf("service %s temporarily unavailable (circuit open)", job.Service))
		return
	}

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	...
```

Record outcomes: in the `complete` branch (after the track-count check passes), call `h.breaker.RecordSuccess(job.Service)`; in `failJob`, call `h.breaker.RecordFailure(job.Service)`. Update `failJob`:

```go
func (h *Handler) failJob(job *queue.Job, errMsg string) {
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	h.queue.Update(job)
	h.queue.MoveToHistory(job.NzoID)
	h.breaker.RecordFailure(job.Service)
	h.log.Error().Str("nzo_id", job.NzoID).Str("error", errMsg).Msg("download failed")
}
```

And in the success path of `processDownload` (right after the track-count check, before `job.Status = sabnzbd.StatusCompleted`):

```go
				h.breaker.RecordSuccess(job.Service)
				job.Status = sabnzbd.StatusCompleted
```

- [ ] **Step 6: Write a handler-level test for the breaker short-circuiting a job**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestProcessDownloadShortCircuitsWhenBreakerOpen(t *testing.T) {
	app, q := setupTestApp(t)
	_ = app

	cfg := &config.Config{OutputDir: t.TempDir(), MaxConcurrent: 1, JobTimeout: 5 * time.Second}
	st := storage.New(cfg.OutputDir)
	client := apispotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	// Force the breaker open by feeding 5 consecutive failures for "tidal".
	for i := 0; i < 5; i++ {
		job := &queue.Job{NzoID: fmt.Sprintf("SABnzbd_nzo_fail%d", i), Service: "tidal", SpotifyURL: "bad"}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job) // "echo" exits 0 with no "complete" event -> fails
	}

	job := &queue.Job{NzoID: "SABnzbd_nzo_shortcircuit", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/x"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 20})
	require.NoError(t, err)
	var found bool
	for _, j := range hist {
		if j.NzoID == "SABnzbd_nzo_shortcircuit" {
			found = true
			assert.Contains(t, j.ErrorMessage, "circuit open")
		}
	}
	assert.True(t, found)
}
```

Add `"fmt"` to the test file's imports if not already present.

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadShortCircuitsWhenBreakerOpen -v`
Expected: PASS

- [ ] **Step 8: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/breaker/ internal/api/sabnzbd/handler.go internal/api/sabnzbd/handler_test.go
git commit -m "feat: add per-service circuit breaker to fast-fail during upstream outages"
```

---

### Task 9: Retry with backoff before failing a job

**Files:**
- Modify: `internal/api/sabnzbd/handler.go` (`processDownload` retries the CLI invocation)

**Interfaces:**
- Consumes: `h.client.Download(...)` (existing), `h.breaker` from Task 8
- Produces: no new exported interface — internal retry loop

- [ ] **Step 1: Write the failing test**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestProcessDownloadRetriesBeforeFailing(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 5 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Script counts invocations via a marker file; fails first 2 times, succeeds 3rd.
	counterFile := filepath.Join(t.TempDir(), "count")
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
COUNT=0
if [[ -f "` + counterFile + `" ]]; then COUNT=$(cat "` + counterFile + `"); fi
COUNT=$((COUNT+1))
echo $COUNT > "` + counterFile + `"
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$COUNT" -lt 3 ]]; then
  echo '{"type":"error","message":"transient failure"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_retry001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/retry"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabnzbd.StatusCompleted, hist[0].Status, "job should succeed on the 3rd attempt after 2 retries")
}
```

Note: this test requires 2 retries (3 total attempts), so Step 3's retry count must be set to allow 3 total attempts.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadRetriesBeforeFailing -v`
Expected: FAIL — job marked `Failed` after the first attempt, no retry happens

- [ ] **Step 3: Implement retry loop in `processDownload`**

Replace the body of `processDownload` from the `ctx := context.Background()` line onward with a retry-wrapping loop. Full replacement of `processDownload`:

```go
const maxAttempts = 3

var retryBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	if !h.breaker.Allow(job.Service) {
		h.failJob(job, fmt.Sprintf("service %s temporarily unavailable (circuit open)", job.Service))
		return
	}

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		h.failJob(job, err.Error())
		return
	}

	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	h.queue.Update(job)

	var lastErr string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ok, errMsg := h.attemptDownload(job, jobDir)
		if ok {
			return
		}
		lastErr = errMsg
		if attempt < maxAttempts {
			h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Str("error", errMsg).Msg("download attempt failed, retrying")
			time.Sleep(retryBackoff[attempt-1])
		}
	}
	h.failJob(job, lastErr)
}

// attemptDownload runs a single CLI invocation and reports whether it
// succeeded. On success it fully updates the job to Completed and moves it
// to history itself (mirroring the previous inline behavior); on failure it
// returns false with the error message and leaves the job untouched for the
// caller to retry or ultimately fail.
func (h *Handler) attemptDownload(job *queue.Job, jobDir string) (bool, string) {
	ctx := context.Background()
	events, errs := h.client.Download(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

	sawComplete := false
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				if !sawComplete {
					return false, "cli exited without completion signal"
				}
				return true, ""
			}
			if evt.Type == "progress" {
				job.Percentage = evt.Percent
				job.Sizeleft = int64(float64(job.Size) * (100 - evt.Percent) / 100)
				h.queue.Update(job)
			}
			if evt.Type == "complete" {
				sawComplete = true
				if job.TrackCount > 0 {
					gotCount, cerr := storage.CountAudioFiles(evt.OutputPath)
					if cerr != nil || gotCount < job.TrackCount {
						return false, fmt.Sprintf("partial album: %d/%d tracks", gotCount, job.TrackCount)
					}
				}
				h.breaker.RecordSuccess(job.Service)
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
				return true, ""
			}
			if evt.Type == "metadata" {
				job.Filename = evt.Artist + " - " + evt.Album
				h.queue.Update(job)
			}
		case e, ok := <-errs:
			if !ok {
				continue
			}
			if e != nil {
				return false, e.Error()
			}
		}
	}
}
```

Note the `case e, ok := <-errs: if !ok { continue }` — unlike the original code's `if !ok { return }`, here a closed `errs` channel with no error must NOT terminate the loop early, because `events` is the channel that signals true completion; `continue` lets the `select` keep waiting on `events` (a closed channel `errs` will now always take the `!ok` branch instantly in a tight loop only if `events` is also closed or never selected — since both channels close together per `spotiflac.Client.Download`'s `defer` block, this is safe: once both are closed, the next `select` iteration hits `events`'s `!ok` branch, which returns).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadRetriesBeforeFailing -v`
Expected: PASS

- [ ] **Step 5: Run full suite (this refactor touches every existing processDownload test)**

Run: `go build ./... && go test ./... -count=1 -timeout 120s`
Expected: PASS — note `TestDownloadTimeout`-style tests and the circuit-breaker test from Task 8 must still pass; the retry loop adds up to 20s of sleep in worst-case failure tests, so if `TestProcessDownloadShortCircuitsWhenBreakerOpen` (Task 8) now takes too long (5 failing attempts × up to 3 retries × backoff), reduce that test's job count or accept the longer runtime — do not reduce `maxAttempts` or `retryBackoff` in production code to make tests faster.

- [ ] **Step 6: Commit**

```bash
git add internal/api/sabnzbd/handler.go internal/api/sabnzbd/handler_test.go
git commit -m "feat: retry failed downloads up to 3 attempts with backoff before failing the job"
```

---

### Task 10: Quality/service fallback chain

**Files:**
- Modify: `internal/config/config.go` (add `FallbackServices []string` field + env parsing)
- Modify: `internal/api/sabnzbd/handler.go` (`attemptDownload`/`processDownload` try next service in chain once)
- Modify: `.env.example`
- Test: `internal/config/config_test.go`, `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: `config.Config` from Task 9's call sites
- Produces: `config.Config.FallbackServices []string` — consumed only within Task 10

- [ ] **Step 1: Write the failing config test**

Add to `internal/config/config_test.go`:

```go
func TestFallbackServicesParsing(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_FALLBACK_SERVICES", "tidal, qobuz ,amazon")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"tidal", "qobuz", "amazon"}, cfg.FallbackServices)
}

func TestFallbackServicesDefaultEmpty(t *testing.T) {
	t.Setenv("SPF_API_KEY", "test")
	t.Setenv("SPF_OUTPUT_DIR", t.TempDir())
	t.Setenv("SPF_DB_PATH", filepath.Join(t.TempDir(), "q.db"))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.FallbackServices)
}
```

Add `"path/filepath"` and `"github.com/stretchr/testify/require"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestFallbackServices -v`
Expected: FAIL — `cfg.FallbackServices` undefined (compile error)

- [ ] **Step 3: Implement `FallbackServices` config**

In `internal/config/config.go`, add the field to `Config`:

```go
type Config struct {
	Port             int           `mapstructure:"port"`
	APIKey           string        `mapstructure:"api_key"`
	OutputDir        string        `mapstructure:"output_dir"`
	SpotiflacCLIPath string        `mapstructure:"spotiflac_cli_path"`
	DefaultService   string        `mapstructure:"default_service"`
	DefaultQuality   string        `mapstructure:"default_quality"`
	MaxConcurrent    int           `mapstructure:"max_concurrent"`
	JobTimeout       time.Duration `mapstructure:"job_timeout"`
	DBPath           string        `mapstructure:"db_path"`
	LogLevel         string        `mapstructure:"log_level"`
	FallbackServices []string      `mapstructure:"-"`
}
```

(`mapstructure:"-"` because it needs custom comma-split parsing, not Viper's default string handling — set it manually after `Unmarshal`.)

In `Load()`, after the existing `v.BindEnv(...)` loop and before `cfg, err := ...; v.Unmarshal(cfg)`, add `"fallback_services"` to the `BindEnv` list, then parse it manually after unmarshal:

```go
	for _, key := range []string{
		"api_key", "port", "output_dir", "spotiflac_cli_path",
		"default_service", "default_quality", "max_concurrent",
		"job_timeout", "db_path", "log_level", "fallback_services",
	} {
		v.BindEnv(key)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if raw := v.GetString("fallback_services"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				cfg.FallbackServices = append(cfg.FallbackServices, s)
			}
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -run TestFallbackServices -v`
Expected: PASS

- [ ] **Step 5: Write the failing handler test for fallback behavior**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestProcessDownloadFallsBackToNextService(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second,
		FallbackServices: []string{"tidal", "qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Script fails for tidal, succeeds for qobuz (service passed as --service flag).
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
SERVICE=""
OUTDIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --output-dir) OUTDIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTDIR"
if [[ "$SERVICE" == "tidal" ]]; then
  echo '{"type":"error","message":"tidal unavailable"}'
  exit 1
fi
touch "$OUTDIR/01.flac"
echo '{"type":"complete","path":"'"$OUTDIR"'","size":1000}'
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless")
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_fallback001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/fb"}
	require.NoError(t, q.Add(job))
	handler.ProcessDownloadSync(job)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabnzbd.StatusCompleted, hist[0].Status)
	assert.Equal(t, "qobuz", hist[0].Service, "job's recorded service should reflect the fallback that actually succeeded")
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadFallsBackToNextService -v`
Expected: FAIL — job fails on tidal, no fallback attempted (also 3 retries against a script that always fails for tidal takes ~20s and still fails)

- [ ] **Step 7: Implement fallback in `processDownload`**

Replace the retry loop in `processDownload` (from Task 9) with a version that, after exhausting retries for the current service, tries the next configured fallback service once (with its own fresh `maxAttempts` retry budget would be excessive — per the spec, "a single fallback attempt per job, not a full cross-product retry matrix" — so the fallback service gets exactly 1 attempt, no retries):

```go
func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	if !h.breaker.Allow(job.Service) {
		h.failJob(job, fmt.Sprintf("service %s temporarily unavailable (circuit open)", job.Service))
		return
	}

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		h.failJob(job, err.Error())
		return
	}

	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	h.queue.Update(job)

	lastErr := h.runAttemptsWithRetry(job, jobDir, maxAttempts)
	if lastErr == "" {
		return
	}

	for _, fallbackSvc := range h.fallbackChain(job.Service) {
		if !h.breaker.Allow(fallbackSvc) {
			continue
		}
		h.log.Warn().Str("nzo_id", job.NzoID).Str("from_service", job.Service).Str("to_service", fallbackSvc).Msg("falling back to next service")
		job.Service = fallbackSvc
		h.queue.Update(job)
		if fbErr := h.runAttemptsWithRetry(job, jobDir, 1); fbErr == "" {
			return
		} else {
			lastErr = fbErr
		}
	}

	h.failJob(job, lastErr)
}

// runAttemptsWithRetry runs up to `attempts` tries of the download, sleeping
// with backoff between them. Returns "" on success, the last error otherwise.
func (h *Handler) runAttemptsWithRetry(job *queue.Job, jobDir string, attempts int) string {
	var lastErr string
	for attempt := 1; attempt <= attempts; attempt++ {
		ok, errMsg := h.attemptDownload(job, jobDir)
		if ok {
			return ""
		}
		lastErr = errMsg
		if attempt < attempts {
			h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Str("error", errMsg).Msg("download attempt failed, retrying")
			time.Sleep(retryBackoff[attempt-1])
		}
	}
	return lastErr
}

// fallbackChain returns the configured fallback services after the given
// current service, preserving configured order, excluding the current one.
func (h *Handler) fallbackChain(current string) []string {
	var chain []string
	for _, svc := range h.cfg.FallbackServices {
		if svc != current {
			chain = append(chain, svc)
		}
	}
	return chain
}
```

Remove the now-superseded standalone retry loop from Task 9 (it's replaced by `runAttemptsWithRetry` + the fallback wrapper above — `attemptDownload` itself is unchanged from Task 9).

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestProcessDownloadFallsBackToNextService -v -timeout 60s`
Expected: PASS

- [ ] **Step 9: Run full suite**

Run: `go build ./... && go test ./... -count=1 -timeout 180s`
Expected: PASS

- [ ] **Step 10: Document the new env var**

Add to `.env.example`, after `SPF_DEFAULT_QUALITY`:

```
# Comma-separated fallback service chain tried once (no retries) if the
# primary service fails after all retries. Empty = disabled (default).
# Example: SPF_FALLBACK_SERVICES=tidal,qobuz,amazon,deezer
SPF_FALLBACK_SERVICES=
```

- [ ] **Step 11: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/api/sabnzbd/handler.go internal/api/sabnzbd/handler_test.go .env.example
git commit -m "feat: try next configured service once as a fallback before failing a job"
```

---

### Task 11: Estimate release size instead of hardcoded 0

**Files:**
- Modify: `internal/indexer/newznab.go`
- Test: `internal/indexer/newznab_test.go` (new file)

**Interfaces:**
- Consumes: `spotiflac.MetadataResult.TrackCount`
- Produces: `indexer.EstimateSizeBytes(trackCount int, quality string) int64` — used by no other task

- [ ] **Step 1: Write the failing test**

Create `internal/indexer/newznab_test.go`:

```go
package indexer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestEstimateSizeBytes(t *testing.T) {
	assert.Equal(t, int64(0), indexer.EstimateSizeBytes(0, "lossless"))
	assert.Greater(t, indexer.EstimateSizeBytes(10, "lossless"), int64(0))
	assert.Greater(t, indexer.EstimateSizeBytes(10, "hires"), indexer.EstimateSizeBytes(10, "lossless"),
		"hires estimate should be larger per track than lossless")
}

func TestNewznabXMLPopulatesNonZeroSize(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", TrackCount: 12},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	assert.NoError(t, err)
	assert.NotContains(t, string(xml), `name="size" value="0"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/indexer/... -v`
Expected: FAIL — `indexer.EstimateSizeBytes` undefined

- [ ] **Step 3: Implement the estimate and wire it into `NewznabXML`**

Add to `internal/indexer/newznab.go`:

```go
const (
	avgBytesPerTrackLossless = 35 * 1024 * 1024 // ~35MB/track, 16-bit FLAC estimate
	avgBytesPerTrackHires    = 90 * 1024 * 1024 // ~90MB/track, 24-bit hi-res FLAC estimate
)

// EstimateSizeBytes gives a rough, clearly-approximate release size so
// Lidarr's size-based checks don't see a hard 0. Not exact — SpotiFLAC's
// search output doesn't expose real payload size ahead of download.
func EstimateSizeBytes(trackCount int, quality string) int64 {
	if trackCount <= 0 {
		return 0
	}
	perTrack := int64(avgBytesPerTrackLossless)
	if quality == "hires" {
		perTrack = avgBytesPerTrackHires
	}
	return int64(trackCount) * perTrack
}
```

In `NewznabXML`, replace:

```go
		attrs := []Attr{
			{Name: "artist", Value: r.Artist},
			{Name: "album", Value: r.Album},
			{Name: "genre", Value: r.Genre},
			{Name: "year", Value: fmt.Sprintf("%d", r.Year)},
			{Name: "title", Value: r.Artist + " - " + r.Album},
			{Name: "size", Value: "0"},
			{Name: "grabs", Value: "0"},
			{Name: "files", Value: fmt.Sprintf("%d", r.TrackCount)},
			{Name: "poster", Value: r.CoverURL},
		}
```

with:

```go
		estimatedSize := EstimateSizeBytes(r.TrackCount, "lossless")
		attrs := []Attr{
			{Name: "artist", Value: r.Artist},
			{Name: "album", Value: r.Album},
			{Name: "genre", Value: r.Genre},
			{Name: "year", Value: fmt.Sprintf("%d", r.Year)},
			{Name: "title", Value: r.Artist + " - " + r.Album},
			{Name: "size", Value: fmt.Sprintf("%d", estimatedSize)},
			{Name: "grabs", Value: "0"},
			{Name: "files", Value: fmt.Sprintf("%d", r.TrackCount)},
			{Name: "poster", Value: r.CoverURL},
		}
```

and:

```go
			Enclosure: Enclosure{
				URL:    r.SpotifyURL,
				Length: 0,
				Type:   "application/x-nzb",
			},
```

with:

```go
			Enclosure: Enclosure{
				URL:    r.SpotifyURL,
				Length: estimatedSize,
				Type:   "application/x-nzb",
			},
```

(`estimatedSize` is quality-agnostic here — `MetadataResult` doesn't carry a quality field at search time, so the estimate always uses `"lossless"`; this is a labeled approximation per the design spec, not exact.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/indexer/... -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/indexer/newznab.go internal/indexer/newznab_test.go
git commit -m "fix: estimate release size instead of always reporting 0 bytes"
```

---

## Phase 2 — Security

### Task 12: Constant-time API key comparison, remove dead auth function

**Files:**
- Modify: `internal/api/middleware.go`
- Test: create `internal/api/middleware_test.go` (package currently has zero tests)

**Interfaces:**
- Consumes: nothing new
- Produces: no interface change — `APIKeyAuthWithSkiplist` signature unchanged; `APIKeyAuth` deleted (dead code, unused per grep of `cmd/server/main.go`)

- [ ] **Step 1: Write the failing test**

Create `internal/api/middleware_test.go`:

```go
package api_test

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
)

func TestAPIKeyAuthWithSkiplistAcceptsCorrectKey(t *testing.T) {
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=correct-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestAPIKeyAuthWithSkiplistRejectsWrongKey(t *testing.T) {
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=wrong-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestAPIKeyAuthWithSkiplistRejectsDifferentLengthKey(t *testing.T) {
	// Regression guard: subtle.ConstantTimeCompare requires equal-length
	// inputs; a naive switch to it would panic or misbehave on length
	// mismatch if not handled explicitly.
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("a-fairly-long-correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=short", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/... -v`
Expected: PASS already for the first two (current `!=` compare works functionally) — this step's real purpose is establishing a baseline before the refactor; the third test also already passes with `!=`. Since there's no *behavioral* red step here (the fix is non-functional, timing-only), skip straight to Step 3 and treat Step 4 as the verification that behavior is unchanged.

- [ ] **Step 3: Replace the comparison with a constant-time one, delete dead `APIKeyAuth`**

In `internal/api/middleware.go`, delete the entire `APIKeyAuth` function (lines 8-23 — confirmed unused via `grep -rn "api.APIKeyAuth(" cmd/ internal/` returning no matches, only `APIKeyAuthWithSkiplist` is called from `cmd/server/main.go`).

In `APIKeyAuthWithSkiplist`, replace:

```go
		if key != apiKey {
```

with:

```go
		if subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
```

Add `"crypto/subtle"` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/... -v`
Expected: PASS (all three)

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/middleware.go internal/api/middleware_test.go
git commit -m "security: use constant-time API key comparison, remove unused APIKeyAuth"
```

---

### Task 13: Redact API key from request logs

**Files:**
- Modify: `internal/api/middleware.go`
- Test: `internal/api/middleware_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: no interface change

- [ ] **Step 1: Write the failing test**

Add to `internal/api/middleware_test.go`:

```go
func TestRequestLoggerRedactsAPIKey(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)

	app := fiber.New()
	app.Use(api.RequestLogger(log))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?mode=queue&apikey=super-secret-value", nil)
	_, err := app.Test(req)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "super-secret-value")
	assert.Contains(t, buf.String(), "apikey=***")
}
```

Add `"bytes"` and `"github.com/rs/zerolog"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/... -run TestRequestLoggerRedactsAPIKey -v`
Expected: FAIL — log line contains `super-secret-value`

- [ ] **Step 3: Implement redaction**

In `internal/api/middleware.go`, replace `RequestLogger`:

```go
func RequestLogger(log zerolog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()
		log.Info().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Str("query", redactAPIKey(string(c.Request().URI().QueryString()))).
			Int("status", c.Response().StatusCode()).
			Msg("request")
		return err
	}
}

// redactAPIKey replaces the value of an `apikey` query parameter with `***`
// so request logs never contain the actual secret.
func redactAPIKey(query string) string {
	values, err := url.ParseQuery(query)
	if err != nil {
		return query
	}
	if values.Has("apikey") {
		values.Set("apikey", "***")
	}
	return values.Encode()
}
```

Add `"net/url"` to the import block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/... -run TestRequestLoggerRedactsAPIKey -v`
Expected: PASS

Note: `url.Values.Encode()` alphabetizes and percent-encodes parameters, which changes formatting (e.g., `mode=queue&apikey=***` may reorder to `apikey=%2A%2A%2A&mode=queue`); the test only asserts substrings, so this is fine, but be aware the logged query string's parameter order changes from the original request. `***` percent-encodes to `%2A%2A%2A` — adjust the test assertion if needed:

```go
	assert.Contains(t, buf.String(), "apikey=%2A%2A%2A")
```

Use this corrected assertion instead of `apikey=***` in Step 1's test.

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/middleware.go internal/api/middleware_test.go
git commit -m "security: redact apikey query parameter from request logs"
```

---

### Task 14: Validate Spotify URL before it reaches the CLI

**Files:**
- Create: `internal/config/spotify_url.go`
- Modify: `internal/api/sabnzbd/addurl.go`
- Test: `internal/config/spotify_url_test.go`, `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `config.IsValidSpotifyURL(url string) bool` — used only in `addurl.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/spotify_url_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

func TestIsValidSpotifyURL(t *testing.T) {
	valid := []string{
		"https://open.spotify.com/album/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/track/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/playlist/4aawyAB9vmqN3uQ7FjRGTy",
		"https://open.spotify.com/intl-de/album/4aawyAB9vmqN3uQ7FjRGTy",
	}
	for _, u := range valid {
		assert.True(t, config.IsValidSpotifyURL(u), u)
	}

	invalid := []string{
		"",
		"not-a-url",
		"https://evil.com/album/x",
		"--output-dir",
		"-x",
		"https://open.spotify.com/artist/4aawyAB9vmqN3uQ7FjRGTy", // artist URLs not albums/tracks/playlists
		"https://open.spotify.com/album/../../etc/passwd",
	}
	for _, u := range invalid {
		assert.False(t, config.IsValidSpotifyURL(u), u)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestIsValidSpotifyURL -v`
Expected: FAIL — `config.IsValidSpotifyURL` undefined

- [ ] **Step 3: Implement the validator**

Create `internal/config/spotify_url.go`:

```go
package config

import "regexp"

var spotifyURLPattern = regexp.MustCompile(`^https://open\.spotify\.com/(intl-[a-z]{2}(-[A-Z]{2})?/)?(track|album|playlist)/[A-Za-z0-9]+(\?.*)?$`)

// IsValidSpotifyURL reports whether url is a well-formed Spotify track,
// album, or playlist link. This is enforced before the URL is ever passed
// as a CLI argument to spotiflac-cli, closing the argument-injection vector
// where an arbitrary string (e.g. "--output-dir") could otherwise reach the
// subprocess's argv as the --url value.
func IsValidSpotifyURL(url string) bool {
	return spotifyURLPattern.MatchString(url)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -run TestIsValidSpotifyURL -v`
Expected: PASS

- [ ] **Step 5: Write the failing handler-level test**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestAddURLRejectsInvalidSpotifyURL(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=--output-dir&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)

	var r sabtypes.StatusResponse
	json.NewDecoder(resp.Body).Decode(&r)
	assert.False(t, r.Status)
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestAddURLRejectsInvalidSpotifyURL -v`
Expected: FAIL — currently returns 200, job created with the malicious value

- [ ] **Step 7: Wire validation into `handleAddURL`**

In `internal/api/sabnzbd/addurl.go`, right after the existing `spotifyURL == ""` check, add:

```go
	if !config.IsValidSpotifyURL(spotifyURL) {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "invalid Spotify URL: must be a https://open.spotify.com/(track|album|playlist)/... link",
		})
	}
```

(`config` is already imported per Task 2.)

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestAddURLRejectsInvalidSpotifyURL -v`
Expected: PASS

- [ ] **Step 9: Run full suite — check existing tests still use valid-looking URLs**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS. If any existing test in `handler_test.go` used a URL not matching the new pattern (e.g. a bare `test123` path segment), update that test's URL to a compliant one like `https://open.spotify.com/album/4aawyAB9vmqN3uQ7FjRGTy` — check `TestAddURL` (`.../album/test123` — this already matches `[A-Za-z0-9]+`, so it passes unchanged) and `TestAddURLDedupReturnsExistingNzoID`/`TestChangeCatUpdatesServiceAndQuality`-style tests from earlier tasks that used `.../album/test`, `.../album/duptest`, `.../album/fb`, `.../album/retry`, `.../album/x` — all alphanumeric path segments, all already valid under the new regex. No changes needed.

- [ ] **Step 10: Commit**

```bash
git add internal/config/spotify_url.go internal/config/spotify_url_test.go internal/api/sabnzbd/addurl.go internal/api/sabnzbd/handler_test.go
git commit -m "security: validate Spotify URL format before passing it to spotiflac-cli"
```

---

### Task 15: Pin SpotiFLAC fork build, run container as non-root

**Files:**
- Modify: `Dockerfile`
- Modify: `renovate.json` (add a rule so Renovate tracks the pinned SHA, if the config format supports git-ref pinning tracking — see Step 3)

**Interfaces:**
- Consumes: nothing
- Produces: nothing — infra-only change

- [ ] **Step 1: Pin the SpotiFLAC fork clone to a fixed commit**

In `Dockerfile`, replace:

```dockerfile
# Stage 2: Build spotiflac-cli from fork (requires Go 1.26)
FROM golang:1.26-alpine AS cli-builder
RUN apk add --no-cache git
RUN git clone https://github.com/fishingpvalues/SpotiFLAC.git /spotiflac
WORKDIR /spotiflac
RUN CGO_ENABLED=0 go build -tags headless -ldflags="-s -w" -o /out/spotiflac-cli .
```

with:

```dockerfile
# Stage 2: Build spotiflac-cli from fork (requires Go 1.26)
FROM golang:1.26-alpine AS cli-builder
ARG SPOTIFLAC_COMMIT=289920c9755f9426175ba88ab2ac0ae45ab8f7d0
RUN apk add --no-cache git
RUN git clone https://github.com/fishingpvalues/SpotiFLAC.git /spotiflac && \
    cd /spotiflac && git checkout ${SPOTIFLAC_COMMIT}
WORKDIR /spotiflac
RUN CGO_ENABLED=0 go build -tags headless -ldflags="-s -w" -o /out/spotiflac-cli .
```

- [ ] **Step 2: Add a non-root user to the runtime stage**

In `Dockerfile`, replace:

```dockerfile
# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=cli-builder /out/spotiflac-cli /usr/local/bin/spotiflac-cli
RUN mkdir -p /downloads /data
EXPOSE 8484
ENTRYPOINT ["server", "serve"]
```

with:

```dockerfile
# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S spotiflac && adduser -S spotiflac -G spotiflac
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=cli-builder /out/spotiflac-cli /usr/local/bin/spotiflac-cli
RUN mkdir -p /downloads /data && chown -R spotiflac:spotiflac /downloads /data
USER spotiflac
EXPOSE 8484
ENTRYPOINT ["server", "serve"]
```

- [ ] **Step 3: Verify the image still builds**

Run: `docker build -t spotiflac-lidarr-proxy:hardening-test .`
Expected: build succeeds; `docker run --rm spotiflac-lidarr-proxy:hardening-test whoami` (override entrypoint: `docker run --rm --entrypoint whoami spotiflac-lidarr-proxy:hardening-test`) prints `spotiflac`, not `root`.

Note: if the CI environment used to execute this plan has no Docker daemon available, skip the `docker build` verification and rely on the `ci.yml` workflow's existing multi-arch dry-run build (`push: false`) to catch any Dockerfile syntax error on the next PR push — do not skip the Dockerfile edits themselves.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile
git commit -m "security: pin SpotiFLAC fork build to a fixed commit, run container as non-root"
```

---

### Task 16: Document reverse-proxy/TLS expectations

**Files:**
- Modify: `README.md`

**Interfaces:** none — docs only

- [ ] **Step 1: Add a security section to the README**

In `README.md`, after the `## Configuration` section and before `## Category System`, add:

```markdown
## Security Notes

This proxy has no built-in TLS support — it's designed to run on a trusted
internal network (the same docker network as Lidarr) and speaks plain HTTP.

- **Do not expose the proxy's port directly to the internet.** If you need
  remote access, put it behind a reverse proxy (Caddy, Traefik, nginx) that
  terminates TLS, the same way you would for Lidarr itself.
- The `SPF_API_KEY` value travels in every request's query string. Over
  plain HTTP on an untrusted network this is readable by anyone on-path —
  another reason to keep this behind a reverse proxy or restrict it to a
  private network.

Example Caddy sidecar snippet for `docker-compose.yml`:

```yaml
services:
  caddy:
    image: caddy:2-alpine
    ports: ["443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
    depends_on: [proxy]
```

```
# Caddyfile
proxy.yourdomain.com {
    reverse_proxy proxy:8484
}
```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: document reverse-proxy/TLS expectations"
```

---

## Phase 3 — Observability

### Task 17: `/metrics` Prometheus endpoint

**Files:**
- Create: `internal/metrics/metrics.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/api/sabnzbd/handler.go` (record metrics at job completion/failure)
- Modify: `go.mod` (add `github.com/prometheus/client_golang`)
- Test: `internal/metrics/metrics_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `metrics.RecordJobResult(status, service string)`, `metrics.RecordDownloadDuration(service, quality string, seconds float64)`, `metrics.SetQueueDepth(status string, count int)`, `metrics.Handler() fiber.Handler` — consumed by `handler.go` and `main.go`

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/prometheus/client_golang@latest`
Expected: `go.mod`/`go.sum` updated with `github.com/prometheus/client_golang` and its transitive deps.

- [ ] **Step 2: Write the failing test**

Create `internal/metrics/metrics_test.go`:

```go
package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"
)

func TestMetricsHandlerExposesJobCounter(t *testing.T) {
	metrics.RecordJobResult("Completed", "tidal")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.PromHTTPHandler().ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `spf_jobs_total{service="tidal",status="Completed"}`)
}

func TestSetQueueDepth(t *testing.T) {
	metrics.SetQueueDepth("Queued", 5)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.PromHTTPHandler().ServeHTTP(rec, req)

	assert.True(t, strings.Contains(rec.Body.String(), `spf_queue_depth{status="Queued"} 5`))
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/metrics/... -v`
Expected: FAIL — package doesn't exist

- [ ] **Step 4: Implement the metrics package**

Create `internal/metrics/metrics.go`:

```go
// Package metrics exposes Prometheus counters/histograms/gauges for the
// proxy's job lifecycle, registered against the default global registry.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	jobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "spf_jobs_total",
		Help: "Total number of jobs by terminal status and service.",
	}, []string{"status", "service"})

	downloadDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "spf_download_duration_seconds",
		Help:    "Download duration in seconds by service and quality.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s .. ~68min
	}, []string{"service", "quality"})

	queueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spf_queue_depth",
		Help: "Current number of jobs in the active queue by status.",
	}, []string{"status"})
)

func RecordJobResult(status, service string) {
	jobsTotal.WithLabelValues(status, service).Inc()
}

func RecordDownloadDuration(service, quality string, seconds float64) {
	downloadDuration.WithLabelValues(service, quality).Observe(seconds)
}

func SetQueueDepth(status string, count int) {
	queueDepth.WithLabelValues(status).Set(float64(count))
}

// PromHTTPHandler returns the standard net/http handler for /metrics.
func PromHTTPHandler() http.Handler {
	return promhttp.Handler()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/metrics/... -v`
Expected: PASS

- [ ] **Step 6: Wire `/metrics` into the fiber app**

In `cmd/server/main.go`, add after the `/health` route registration:

```go
	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	app.Get("/metrics", func(c fiber.Ctx) error {
		return fiberadaptor.HTTPHandler(metrics.PromHTTPHandler())(c)
	})
```

Fiber v3 needs an adapter to run a stdlib `http.Handler`. Add the dependency:

Run: `go get github.com/gofiber/fiber/v3/middleware/adaptor@latest`

Add imports to `cmd/server/main.go`:

```go
	fiberadaptor "github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"
```

- [ ] **Step 7: Record job outcomes from the handler**

In `internal/api/sabnzbd/handler.go`, in `failJob`, add a metrics call:

```go
func (h *Handler) failJob(job *queue.Job, errMsg string) {
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	h.queue.Update(job)
	h.queue.MoveToHistory(job.NzoID)
	h.breaker.RecordFailure(job.Service)
	metrics.RecordJobResult(string(sabnzbd.StatusFailed), job.Service)
	h.log.Error().Str("nzo_id", job.NzoID).Str("error", errMsg).Msg("download failed")
}
```

And in `attemptDownload`'s success branch, right after `h.breaker.RecordSuccess(job.Service)`:

```go
				h.breaker.RecordSuccess(job.Service)
				metrics.RecordJobResult(string(sabnzbd.StatusCompleted), job.Service)
				if !job.TimeAdded.IsZero() {
					metrics.RecordDownloadDuration(job.Service, job.Quality, time.Since(job.TimeAdded).Seconds())
				}
```

Add import `"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"` to `internal/api/sabnzbd/handler.go`.

- [ ] **Step 8: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS. If the fiber v3 `adaptor` package name or import path differs from what's assumed above, check the actual installed version's package doc via `go doc github.com/gofiber/fiber/v3/middleware/adaptor` after `go get` and adjust the import/function name accordingly — do not guess further, verify against the installed module.

- [ ] **Step 9: Commit**

```bash
git add internal/metrics/ cmd/server/main.go internal/api/sabnzbd/handler.go go.mod go.sum
git commit -m "feat: add /metrics Prometheus endpoint with job/duration/queue-depth metrics"
```

---

### Task 18: Real `/health` endpoint

**Files:**
- Modify: `cmd/server/main.go`
- Test: `tests/integration/main_test.go` is gated behind Docker — instead add a lightweight fiber-level test in a new `cmd/server` test is awkward since `main.go` is `package main`; put the health-check logic in a small helper package instead for testability.
- Create: `internal/health/health.go`
- Create: `internal/health/health_test.go`

**Interfaces:**
- Consumes: `queue.SQLiteQueue` (needs a `Ping`-style check — see Step 3), `storage.Storage.GetDiskSpace()`
- Produces: `health.Check(db *sql.DB, cliPath string, st *storage.Storage) health.Result` — consumed by `cmd/server/main.go`

- [ ] **Step 1: Write the failing test**

Create `internal/health/health_test.go`:

```go
package health_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/health"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

func TestCheckHealthyWhenEverythingOK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("#!/bin/bash\necho ok\n"), 0755))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.True(t, result.Healthy)
	assert.Empty(t, result.FailedChecks)
}

func TestCheckUnhealthyWhenCLIMissing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	st := storage.New(dir)

	result := health.Check(db, filepath.Join(dir, "does-not-exist"), st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "cli_executable")
}

func TestCheckUnhealthyWhenCLINotExecutable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("not executable"), 0644))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "cli_executable")
}

func TestCheckUnhealthyWhenDBClosed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.Close()

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("#!/bin/bash\necho ok\n"), 0755))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "database")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/health/... -v`
Expected: FAIL — package doesn't exist

- [ ] **Step 3: Implement the health package**

Create `internal/health/health.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/health/... -v`
Expected: PASS

- [ ] **Step 5: Wire it into `/health`**

`internal/queue.SQLiteQueue` wraps a private `db *sql.DB` field with no exported accessor. Add one:

In `internal/queue/queue.go`, add:

```go
// DB exposes the underlying *sql.DB for health checks only.
func (q *SQLiteQueue) DB() *sql.DB {
	return q.db
}
```

In `cmd/server/main.go`, replace:

```go
	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
```

with:

```go
	app.Get("/health", func(c fiber.Ctx) error {
		result := health.Check(q.DB(), cfg.SpotiflacCLIPath, st)
		if !result.Healthy {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unhealthy",
				"failed": result.FailedChecks,
			})
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})
```

Add import `"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/health"` to `cmd/server/main.go`.

- [ ] **Step 6: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/health/ internal/queue/queue.go cmd/server/main.go
git commit -m "feat: real /health check covering DB, CLI executable, and disk space"
```

---

### Task 19: `warnings` endpoint surfaces real state

**Files:**
- Modify: `internal/api/sabnzbd/status.go`
- Modify: `internal/api/sabnzbd/handler.go` (expose breaker via a getter for the handler to query)
- Test: `internal/api/sabnzbd/handler_test.go`

**Interfaces:**
- Consumes: `breaker.Breaker.Status() map[string]breaker.State` from Task 8
- Produces: no new interface — `handleWarnings` behavior change only

- [ ] **Step 1: Write the failing test**

Add to `internal/api/sabnzbd/handler_test.go`:

```go
func TestWarningsSurfacesOpenBreaker(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 2 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	client := apispotiflac.NewClient("false", 2*time.Second, "tidal", "lossless") // "false" always exits 1
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	for i := 0; i < 5; i++ {
		job := &queue.Job{NzoID: fmt.Sprintf("SABnzbd_nzo_warn%d", i), Service: "tidal", SpotifyURL: "https://open.spotify.com/album/x"}
		require.NoError(t, q.Add(job))
		handler.ProcessDownloadSync(job)
	}

	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "version", "auth"))
	handler.RegisterRoutes(app)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=warnings&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)

	var w sabtypes.WarningsResponse
	json.NewDecoder(resp.Body).Decode(&w)
	require.NotEmpty(t, w.Warnings)
	assert.Contains(t, w.Warnings[0].Text, "tidal")
}
```

Add `"github.com/gofiber/fiber/v3"` and `"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"` to the test file's imports if not already present (they are, per the existing `setupTestApp` helper — reuse that pattern; this test builds its own app because `setupTestApp` doesn't expose the handler for direct breaker manipulation... actually simpler: just build handler directly and register routes as shown above).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/sabnzbd/... -run TestWarningsSurfacesOpenBreaker -v -timeout 60s`
Expected: FAIL — `w.Warnings` is empty

- [ ] **Step 3: Implement `handleWarnings`**

In `internal/api/sabnzbd/status.go`, replace:

```go
func (h *Handler) handleWarnings(c fiber.Ctx) error {
	return c.JSON(sabnzbd.WarningsResponse{
		Warnings: []sabnzbd.Warning{},
	})
}
```

with:

```go
func (h *Handler) handleWarnings(c fiber.Ctx) error {
	var warnings []sabnzbd.Warning
	for service, state := range h.breaker.Status() {
		if !state.Open {
			continue
		}
		warnings = append(warnings, sabnzbd.Warning{
			Time: state.OpenedAt.Unix(),
			Type: "ERROR",
			Text: fmt.Sprintf("service %s circuit open, retrying after %s", service, state.RetryAt.Format(time.RFC3339)),
			ID:   "breaker_" + service,
		})
	}
	if warnings == nil {
		warnings = []sabnzbd.Warning{}
	}
	return c.JSON(sabnzbd.WarningsResponse{Warnings: warnings})
}
```

Add `"fmt"` and `"time"` to `internal/api/sabnzbd/status.go`'s imports if not already present (check the existing import block — it currently imports `"strconv"`, `"time"`; add `"fmt"`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/sabnzbd/... -run TestWarningsSurfacesOpenBreaker -v -timeout 60s`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1 -timeout 180s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/sabnzbd/status.go internal/api/sabnzbd/handler_test.go
git commit -m "feat: surface open circuit breakers via the warnings endpoint"
```

---

### Task 20: Persist CLI output for postmortem debugging

**Files:**
- Modify: `internal/spotiflac/client.go` (capture combined stdout+stderr, expose via `Download`'s return or a side-channel)
- Modify: `internal/queue/job.go`, `internal/queue/queue.go` (new `cli_output` column)
- Modify: `internal/api/sabnzbd/handler.go` (`failJob` persists last 4KB)
- Test: `internal/spotiflac/client_test.go`, `internal/queue/queue_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `queue.Job.CLIOutput string` (not JSON-exposed to Lidarr — `json:"-"`), `spotiflac.Client.Download` gains a way to retrieve captured output — see design below

- [ ] **Step 1: Write the failing test for output capture in the client**

Add to `internal/spotiflac/client_test.go`:

```go
func TestDownloadCapturesOutputOnFailure(t *testing.T) {
	responses := []string{
		`{"type":"error","message":"disk full"}`,
	}
	client := spotiflac.NewClient(mockCli(t, responses), 5*time.Second, "tidal", "lossless")

	events, errs := client.Download(context.Background(),
		"https://open.spotify.com/album/test", "/tmp/test-output", "", "")

	for range events {
	}
	var gotErr error
	for e := range errs {
		gotErr = e
	}
	require.Error(t, gotErr)

	var de *spotiflac.DownloadError
	require.ErrorAs(t, gotErr, &de)
	assert.Contains(t, de.RawOutput, "disk full")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/spotiflac/... -run TestDownloadCapturesOutputOnFailure -v`
Expected: FAIL — `de.RawOutput` undefined (compile error) or empty

- [ ] **Step 3: Capture output in the client**

In `internal/spotiflac/progress.go`, add a `RawOutput` field to `DownloadError`:

```go
type DownloadError struct {
	Message   string
	RawOutput string
}

func (e *DownloadError) Error() string {
	return "spotiflac: " + e.Message
}
```

In `internal/spotiflac/client.go`, the `Download` method currently only pipes `stdout` through `parseProgress`. Capture the raw lines alongside parsing. Replace the body from `stdout, err := cmd.StdoutPipe()` onward:

```go
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- fmt.Errorf("stdout pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errs <- fmt.Errorf("start spotiflac: %w", err)
			return
		}

		var outputBuf bytes.Buffer
		tee := io.TeeReader(stdout, &outputBuf)
		parseProgress(tee, events, errs)

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				errs <- fmt.Errorf("spotiflac timed out after %s", c.timeout)
			} else {
				errs <- fmt.Errorf("spotiflac exited: %w", err)
			}
		}
```

with a version that attaches the captured output to any `*DownloadError` emitted by `parseProgress`. Since `parseProgress` already constructs `&DownloadError{Message: event.ErrorMessage}` internally, the cleanest way to attach output without threading a buffer through `parseProgress`'s signature is to keep the buffer local to `Download` and post-process errors as they're forwarded. Restructure: `Download`'s goroutine reads from `events`/`errs` produced by `parseProgress` running against the tee'd reader, then re-emits errors with `RawOutput` populated. Full replacement of the `Download` method body:

```go
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

		cliQuality := config.SpotiflacQuality(quality)

		cmd := exec.CommandContext(ctx, c.cliPath,
			"--url", url,
			"--output-dir", outputDir,
			"--service", service,
			"--quality", cliQuality,
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

		var outputBuf bytes.Buffer
		tee := io.TeeReader(stdout, &outputBuf)

		innerEvents := make(chan ProgressEvent, 32)
		innerErrs := make(chan error, 1)
		parseProgress(tee, innerEvents, innerErrs)
		for evt := range innerEvents {
			events <- evt
		}
		for e := range innerErrs {
			if de, ok := e.(*DownloadError); ok {
				de.RawOutput = lastNBytes(outputBuf.Bytes(), 4096)
			}
			errs <- e
		}

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

func lastNBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
```

Note: `parseProgress` is synchronous (it scans until EOF, then returns — it's not itself launched as a goroutine in the original code either; check `internal/spotiflac/client.go`'s original call `parseProgress(stdout, events, errs)` — it's called directly, blocking until the CLI's stdout closes, which is correct since `events`/`errs` are buffered channels with capacity 32/1). The rewrite above keeps that same synchronous-call structure but interposes `innerEvents`/`innerErrs` so the raw buffer can be attached to errors before forwarding to the real `events`/`errs`. Add `"bytes"` and `"io"` to `internal/spotiflac/client.go`'s imports (`"io"` may already be absent — check the existing import block, which has `"bufio"`, `"context"`, `"encoding/json"`, `"fmt"`, `"os/exec"`, `"time"` — add `"bytes"` and `"io"`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/spotiflac/... -v`
Expected: PASS for `TestDownloadCapturesOutputOnFailure` and all pre-existing tests in this file (they don't depend on `RawOutput`, so no regressions expected)

- [ ] **Step 5: Add `CLIOutput` to the job and persist on failure**

In `internal/queue/job.go`, add:

```go
	TrackCount   int                `json:"track_count"`
	CLIOutput    string             `json:"-"`
```

In `internal/queue/queue.go`'s `migrate`, add the column:

```go
			track_count INTEGER NOT NULL DEFAULT 0,
			cli_output TEXT NOT NULL DEFAULT '',
			is_history INTEGER NOT NULL DEFAULT 0
```

Add `cli_output` to `Add`'s INSERT (column list + `job.CLIOutput` in the values), and to `Update`'s SET clause (`cli_output=?` + `job.CLIOutput` in args) — but NOT to `Get`/`List`/`History`'s SELECT/Scan, since this field is only for direct DB inspection by an operator (per the spec: "accessible only via direct DB inspection... not surfaced in the SABnzbd protocol responses") and adding it to every read path's Scan would require touching every call site for no consumer. Since `Update` writes it and no Go code path reads it back, this is intentionally write-only from the application's perspective — an operator queries it directly with `sqlite3 queue.db "SELECT cli_output FROM jobs WHERE nzo_id = '...'"`.

- [ ] **Step 6: Wire capture into `failJob`**

In `internal/api/sabnzbd/handler.go`, `attemptDownload`'s error return needs to also surface `RawOutput` up to `failJob`. Modify `attemptDownload`'s error-returning branch:

```go
		case e, ok := <-errs:
			if !ok {
				continue
			}
			if e != nil {
				var de *spotiflac.DownloadError
				if errors.As(e, &de) && de.RawOutput != "" {
					job.CLIOutput = de.RawOutput
				}
				return false, e.Error()
			}
		}
```

Add `"errors"` to `internal/api/sabnzbd/handler.go`'s imports.

In `failJob`, persist it:

```go
func (h *Handler) failJob(job *queue.Job, errMsg string) {
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	h.queue.Update(job)
	h.queue.MoveToHistory(job.NzoID)
	h.breaker.RecordFailure(job.Service)
	metrics.RecordJobResult(string(sabnzbd.StatusFailed), job.Service)
	h.log.Error().Str("nzo_id", job.NzoID).Str("error", errMsg).Msg("download failed")
}
```

`job.CLIOutput` is already set on the `job` pointer before `failJob` is called (since `attemptDownload` mutates the same `*queue.Job` passed into `processDownload`), and `h.queue.Update(job)` inside `failJob` already persists all of `job`'s fields including `CLIOutput` once `Update`'s SET clause includes it (from Step 5) — no further wiring needed.

- [ ] **Step 7: Write a queue-level test confirming persistence**

Add to `internal/queue/queue_test.go`:

```go
func TestUpdatePersistsCLIOutput(t *testing.T) {
	q := newTestQueue(t)
	job := &queue.Job{NzoID: "SABnzbd_nzo_clioutput"}
	require.NoError(t, q.Add(job))

	job.CLIOutput = "some raw cli output for postmortem"
	require.NoError(t, q.Update(job))

	var got string
	row := q.DB().QueryRow("SELECT cli_output FROM jobs WHERE nzo_id = ?", "SABnzbd_nzo_clioutput")
	require.NoError(t, row.Scan(&got))
	assert.Equal(t, "some raw cli output for postmortem", got)
}
```

(Uses the `DB()` getter added in Task 18, Step 5.)

- [ ] **Step 8: Run full suite**

Run: `go build ./... && go test ./... -count=1 -timeout 180s`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/spotiflac/client.go internal/spotiflac/progress.go internal/spotiflac/client_test.go internal/queue/job.go internal/queue/queue.go internal/queue/queue_test.go internal/api/sabnzbd/handler.go
git commit -m "feat: persist last 4KB of CLI output on job failure for postmortem debugging"
```

---

## Phase 4 — Matching + Onboarding Docs

### Task 21: Surface ISRC as a newznab attribute

**Files:**
- Modify: `internal/indexer/newznab.go`
- Test: `internal/indexer/newznab_test.go`

**Interfaces:** none new — additive XML attribute only

- [ ] **Step 1: Write the failing test**

Add to `internal/indexer/newznab_test.go`:

```go
func TestNewznabXMLIncludesISRCAttrWhenPresent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x", ISRC: "USABC1234567"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	require.NoError(t, err)
	assert.Contains(t, string(xml), `name="isrc" value="USABC1234567"`)
}

func TestNewznabXMLOmitsISRCAttrWhenAbsent(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "A", Album: "B", SpotifyURL: "https://open.spotify.com/album/x"},
	}
	xml, err := indexer.NewznabXML(results, "http://localhost:8484")
	require.NoError(t, err)
	assert.NotContains(t, string(xml), `name="isrc"`)
}
```

Add `"github.com/stretchr/testify/require"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/indexer/... -run TestNewznabXML -v`
Expected: FAIL — no `isrc` attr present in either case

- [ ] **Step 3: Add the ISRC attribute conditionally**

In `internal/indexer/newznab.go`, in the results loop, after building `attrs`:

```go
		estimatedSize := EstimateSizeBytes(r.TrackCount, "lossless")
		attrs := []Attr{
			{Name: "artist", Value: r.Artist},
			{Name: "album", Value: r.Album},
			{Name: "genre", Value: r.Genre},
			{Name: "year", Value: fmt.Sprintf("%d", r.Year)},
			{Name: "title", Value: r.Artist + " - " + r.Album},
			{Name: "size", Value: fmt.Sprintf("%d", estimatedSize)},
			{Name: "grabs", Value: "0"},
			{Name: "files", Value: fmt.Sprintf("%d", r.TrackCount)},
			{Name: "poster", Value: r.CoverURL},
		}
		if r.ISRC != "" {
			attrs = append(attrs, Attr{Name: "isrc", Value: r.ISRC})
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/indexer/... -run TestNewznabXML -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/indexer/newznab.go internal/indexer/newznab_test.go
git commit -m "feat: surface ISRC as a newznab attribute to aid Lidarr release matching"
```

---

### Task 22: Enforce history retention

**Files:**
- Modify: `internal/config/config.go` (add `HistoryRetentionCount int` config)
- Modify: `internal/queue/queue.go` (add `PruneHistory(keep int) error`)
- Modify: `internal/api/sabnzbd/addurl.go` (opportunistic prune call)
- Modify: `.env.example`
- Test: `internal/queue/queue_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `queue.SQLiteQueue.PruneHistory(keep int) error` — consumed by `addurl.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/queue/queue_test.go`:

```go
func TestPruneHistoryKeepsOnlyMostRecent(t *testing.T) {
	q := newTestQueue(t)

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("SABnzbd_nzo_hist%d", i)
		require.NoError(t, q.Add(&queue.Job{NzoID: id}))
		require.NoError(t, q.MoveToHistory(id))
	}

	require.NoError(t, q.PruneHistory(2))

	hist, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, hist, 2)
}

func TestPruneHistoryZeroMeansUnlimited(t *testing.T) {
	q := newTestQueue(t)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("SABnzbd_nzo_histunlim%d", i)
		require.NoError(t, q.Add(&queue.Job{NzoID: id}))
		require.NoError(t, q.MoveToHistory(id))
	}

	require.NoError(t, q.PruneHistory(0))

	_, total, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, total, "keep=0 should mean no pruning")
}
```

Add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/queue/... -run TestPruneHistory -v`
Expected: FAIL — `q.PruneHistory` undefined

- [ ] **Step 3: Implement `PruneHistory`**

Add to `internal/queue/queue.go`:

```go
// PruneHistory deletes history rows beyond the `keep` most recent
// (by completed_at). keep <= 0 disables pruning (unlimited retention).
func (q *SQLiteQueue) PruneHistory(keep int) error {
	if keep <= 0 {
		return nil
	}
	_, err := q.db.Exec(
		`DELETE FROM jobs WHERE is_history = 1 AND id NOT IN (
			SELECT id FROM jobs WHERE is_history = 1 ORDER BY completed_at DESC LIMIT ?
		)`, keep,
	)
	if err != nil {
		return fmt.Errorf("prune history: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/queue/... -run TestPruneHistory -v`
Expected: PASS

- [ ] **Step 5: Add config and wire opportunistic pruning**

In `internal/config/config.go`, add to `Config`:

```go
	HistoryRetentionCount int `mapstructure:"history_retention_count"`
```

Add `"history_retention_count"` to the `BindEnv` loop and a default in `setDefaults`:

```go
	v.SetDefault("history_retention_count", 500)
```

In `internal/api/sabnzbd/addurl.go`, after the existing dedup check and before creating the new job, add an opportunistic prune (cheap, per the design spec — "no new background goroutine needed"):

```go
	if h.cfg.HistoryRetentionCount > 0 {
		if err := h.queue.PruneHistory(h.cfg.HistoryRetentionCount); err != nil {
			h.log.Warn().Err(err).Msg("history prune failed")
		}
	}
```

- [ ] **Step 6: Document the new env var**

Add to `.env.example`, after `SPF_FALLBACK_SERVICES`:

```
# Number of most-recent history entries to keep; older ones are pruned
# opportunistically on each new download add. 0 = unlimited.
SPF_HISTORY_RETENTION_COUNT=500
```

- [ ] **Step 7: Run full suite**

Run: `go build ./... && go test ./... -count=1 -timeout 180s`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/queue/queue.go internal/queue/queue_test.go internal/api/sabnzbd/addurl.go .env.example
git commit -m "feat: enforce configurable history retention with opportunistic pruning"
```

---

### Task 23: README troubleshooting section for rate-limiting

**Files:**
- Modify: `README.md`

**Interfaces:** none — docs only

- [ ] **Step 1: Add a troubleshooting section**

In `README.md`, after the `## Security Notes` section added in Task 16, add:

```markdown
## Troubleshooting

### Repeated download failures for one service (Tidal/Qobuz/Amazon/Deezer)

SpotiFLAC requires no account or credentials for any of the four backing
services — it reverse-engineers public APIs, not user logins. The most
common real-world failure mode instead is **IP-based rate limiting**: the
upstream SpotiFLAC project's own FAQ confirms metadata/audio fetches can
get rate-limited per IP, recommending a wait or a VPN.

This proxy has a built-in per-service circuit breaker: after 5 consecutive
failures for one service, it stops sending new jobs to that service for 10
minutes and fails them immediately instead of waiting out a full timeout.
Check `GET /api/sabnzbd?mode=warnings` — an open breaker shows up there
with the service name and when it'll retry.

If you see one service's breaker tripping repeatedly, that service is
likely rate-limiting you; either wait it out, or set
`SPF_FALLBACK_SERVICES` so jobs automatically try another service instead.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add troubleshooting guidance for service rate-limiting"
```

---

## Final Verification

- [ ] **Run the full test suite one more time end to end**

Run: `go build ./... && go vet ./... && go test ./... -count=1 -timeout 300s`
Expected: PASS, no vet warnings

- [ ] **Run golangci-lint if available**

Run: `golangci-lint run ./... 2>&1 | head -100` (per `.golangci.yml` already in the repo)
Expected: no new lint findings introduced by this plan's changes (pre-existing findings, if any, are out of scope)

- [ ] **Confirm `openapi.json` doesn't need updates**

The changes in this plan are additive to existing response shapes (new `newznab:attr` values, changed `size`/`length` numeric values, unchanged field names) except `/metrics` and the `/health` response's new `failed` field on the unhealthy path — these aren't part of the SABnzbd/Newznab contract `openapi.json` documents, so no spec update is required. Confirm by grepping: `grep -n "isrc\|metrics\|failed" openapi.json` — expect no matches, confirming these are genuinely new surface, not a documented-but-wrong contract.
