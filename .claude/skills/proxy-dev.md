---
name: proxy-dev
description: Development tasks for the spotiflac-lidarr-proxy Go service
---

# Spotiflac-Lidarr Proxy Development

## Build & Test

```bash
go build ./cmd/server          # Quick build check
go test ./... -count=1         # Run all unit tests
make test-cover                # Coverage report
make lint                      # golangci-lint
```

## Adding a new SABnzbd API endpoint

1. Add handler method to `internal/api/sabnzbd/handler.go`:
   ```go
   func (h *Handler) handleNewMode(c fiber.Ctx) error { ... }
   ```
2. Add dispatch case in `dispatch()` switch
3. Add response type to `pkg/sabnzbd/types.go` if needed
4. Add test in `handler_test.go`
5. Verify against upstream Lidarr Sabnzbd.cs expectations

## Upstream compatibility check

```bash
# Fetch latest Lidarr SABnzbd client
curl -sL https://raw.githubusercontent.com/Lidarr/Lidarr/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs

# Check our handler against what Lidarr expects
grep -oP 'mode=\w+' /tmp/Sabnzbd.cs | sort -u
grep -oP 'case mode == "\w+"' internal/api/sabnzbd/handler.go | sort -u
```

## Key design decisions

- **Float64 for disk/memory sizes:** SABnzbd returns numeric values, Lidarr parses them as numbers. No string formatting in JSON.
- **Auth skiplist:** `version` and `auth` endpoints skip API key check. `caps` endpoint also skips it. All others require valid API key.
- **Job states:** Queued → Downloading → Completed/Failed → MoveToHistory. History items never re-enter queue.
- **SpotiFLAC CLI:** Always outputs JSON on stdout. Parse line-by-line. `track_done` maps to metadata events for progress tracking.

## File responsibilities

| File | Purpose |
|------|---------|
| `pkg/sabnzbd/types.go` | SABnzbd JSON types — must match real SABnzbd API exactly |
| `internal/api/sabnzbd/handler.go` | Router dispatch + download processor + slot conversion |
| `internal/api/sabnzbd/status.go` | version, auth, get_config, get_cats, fullstatus |
| `internal/api/sabnzbd/addurl.go` | addurl/addfile submission |
| `internal/api/sabnzbd/queue.go` | queue listing, pause, resume, delete |
| `internal/api/sabnzbd/history.go` | history listing |
| `internal/api/newznab/handler.go` | Newznab caps/search/music/details dispatch |
| `internal/indexer/newznab.go` | Newznab XML generation (RSS 2.0 + newznab namespace) |
| `internal/indexer/spotify.go` | Spotify metadata search wrapper |
| `internal/spotiflac/client.go` | CLI subprocess execution + SearchMetadata |
| `internal/spotiflac/progress.go` | JSON progress line parsing |
| `internal/queue/job.go` | Job model |
| `internal/queue/queue.go` | SQLite CRUD + history |
| `internal/storage/storage.go` | File system operations + disk space |
| `internal/config/config.go` | Viper env var loading |
| `internal/api/middleware.go` | Auth + request logging |
