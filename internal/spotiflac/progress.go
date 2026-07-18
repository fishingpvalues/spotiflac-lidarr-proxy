package spotiflac

import (
	"bufio"
	"encoding/json"
	"io"
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
		case "track_done":
			// Map track_done to a metadata event so the download processor
			// can extract artist/album info and update progress
			event.Type = "metadata"
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

type DownloadError struct {
	Message string
}

func (e *DownloadError) Error() string {
	return "spotiflac: " + e.Message
}
