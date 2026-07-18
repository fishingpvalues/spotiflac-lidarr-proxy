package spotiflac

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
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

		// Map proxy quality names to SpotiFLAC CLI uppercase flags
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
		var raw struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Artist      string `json:"artist"`
			Album       string `json:"album"`
			SpotifyURL  string `json:"spotify_url"`
			CoverURL    string `json:"cover_url"`
			Year        string `json:"year"`
			TrackCount  int    `json:"track_count"`
			Title string `json:"title"`
			ISRC  string `json:"isrc"`
			Genre string `json:"genre"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		url := raw.SpotifyURL
		if url == "" {
			continue
		}
		title := raw.Title
		if title == "" {
			title = raw.Name
		}
		artist := raw.Artist
		if artist == "" {
			artist = raw.Name
		}
		results = append(results, MetadataResult{
			Artist:     artist,
			Album:      raw.Album,
			Title:      title,
			SpotifyURL: url,
			CoverURL:   raw.CoverURL,
			ISRC:       raw.ISRC,
			Genre:      raw.Genre,
			TrackCount: raw.TrackCount,
		})
	}

	if err := cmd.Wait(); err != nil {
		return results, fmt.Errorf("spotiflac search exited: %w", err)
	}

	return results, nil
}
