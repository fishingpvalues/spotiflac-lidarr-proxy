package spotiflac

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type ProgressEvent struct {
	Type         string  `json:"type"`
	Track        string  `json:"track,omitempty"`
	Title        string  `json:"title,omitempty"`
	Artist       string  `json:"artist,omitempty"`
	Album        string  `json:"album,omitempty"`
	Percent      float64 `json:"percent,omitempty"`
	Speed        string  `json:"speed,omitempty"`
	OutputPath   string  `json:"path,omitempty"`
	Size         int64   `json:"size,omitempty"`
	ISRC         string  `json:"isrc,omitempty"`
	ErrorMessage string  `json:"message,omitempty"`
	// Detail carries the backend's own explanation of a failure - the
	// provider cascade's reason lines. Without it a failed download reached
	// Lidarr as a bare "spotiflac exited: exit status 1".
	Detail string `json:"detail,omitempty"`
	// TrackCount is how many audio files the backend actually produced,
	// reported on the terminal "complete" event.
	TrackCount int    `json:"track_count,omitempty"`
	URL        string `json:"url,omitempty"`
	CB         string `json:"cb,omitempty"`
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
	// Entity is "album", "track" or "" - what the Spotify URL points at.
	// The CLI reports it as `entity`; older builds omit the field, so it is
	// also derived from the URL path. It decides whether a grab downloads a
	// whole album or one file, which is the difference between a release
	// Lidarr can import and one it always rejects.
	Entity string `json:"entity"`
}

// Spotify URL path segments, as they appear in CLI search output.
const (
	EntityAlbum = "album"
	EntityTrack = "track"
)

// EntityKind reports what the result's Spotify URL points at, preferring the
// CLI's own `entity` field and falling back to the URL path for CLI builds that
// predate it.
func (m MetadataResult) EntityKind() string {
	switch m.Entity {
	case EntityAlbum, EntityTrack:
		return m.Entity
	}
	switch {
	case strings.Contains(m.SpotifyURL, "/album/"):
		return EntityAlbum
	case strings.Contains(m.SpotifyURL, "/track/"):
		return EntityTrack
	}
	return ""
}

func parseProgress(reader io.Reader, events chan<- ProgressEvent, errors chan<- error, output *bytes.Buffer, onVerify func(ProgressEvent)) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event ProgressEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		switch event.Type {
		case "error":
			// Both streams matter and neither is sufficient. The backend's
			// captured stderr arrives as `detail`, while the provider
			// cascade prints its per-extension reasons ("ext:tidal-web:
			// NETWORK_ERROR: Timeout (120s) calling download") to stdout,
			// which only this buffer has. Reporting one and dropping the
			// other loses the reason about half the time.
			errors <- &DownloadError{
				Message:   event.ErrorMessage,
				RawOutput: joinNonEmpty(event.Detail, lastNBytes(output.Bytes(), 4096)),
			}
		case "complete":
			events <- event
		case "track_done":
			// Map track_done to a metadata event so the download processor
			// can extract artist/album info and update progress
			event.Type = "metadata"
			events <- event
		case "verification_required":
			// SpotiFLAC headless build emits this when community verification
			// is needed. The URL is pre-rewritten with verify_relay_url
			// as the cb= parameter and upstream_cb= pointing at the local
			// callback server.
			if onVerify != nil {
				onVerify(event)
			}
			events <- event
		case "status", "progress", "metadata":
			events <- event
		default:
			events <- event
		}
	}
	if err := scanner.Err(); err != nil {
		errors <- err
	}
}

func joinNonEmpty(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

func lastNBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}

type DownloadError struct {
	Message   string
	RawOutput string
}

func (e *DownloadError) Error() string {
	return "spotiflac: " + e.Message
}
