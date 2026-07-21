package indexer

import (
	"context"
	"strings"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func Search(ctx context.Context, client *spotiflac.Client, query, artist, album string) ([]spotiflac.MetadataResult, error) {
	searchQuery := query
	if artist != "" && album != "" {
		searchQuery = artist + " " + album
	} else if artist != "" {
		searchQuery = artist
	}

	results, err := client.SearchMetadata(ctx, searchQuery)
	if err != nil {
		return results, err
	}

	return filterResults(results, artist, album), nil
}

// filterResults drops results Lidarr could never use as an album release,
// and results whose Artist/Album don't actually match what was asked for.
//
// Two distinct problems observed against production:
//  1. Spotify's search matches query terms anywhere (track title, artist,
//     album), so an artist-only search for e.g. "Lily Phillips" surfaced an
//     unrelated song merely *titled* "Lily Phillips" by a different artist.
//  2. Some track hits have no resolvable containing album (Album == ""),
//     rendering as a "{Artist} - " newznab title that Lidarr's parser
//     rejects outright as unparseable - pure noise at best, and a
//     structurally invalid "release" at worst.
func filterResults(results []spotiflac.MetadataResult, artist, album string) []spotiflac.MetadataResult {
	filtered := make([]spotiflac.MetadataResult, 0, len(results))
	for _, r := range results {
		if r.Album == "" {
			continue
		}
		if artist != "" && !strings.Contains(strings.ToLower(r.Artist), strings.ToLower(artist)) {
			continue
		}
		if album != "" && !strings.Contains(strings.ToLower(r.Album), strings.ToLower(album)) {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}
