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

// filterResults drops results whose Artist/Album don't actually match what
// was asked for. Spotify's search matches query terms anywhere (track
// title, artist, album), so an artist-only search for e.g. "Lily Phillips"
// can surface an unrelated song merely *titled* "Lily Phillips" by a
// different artist. Lidarr expects an artist/album-scoped search to return
// only matches for that artist/album, not a fuzzy keyword search.
func filterResults(results []spotiflac.MetadataResult, artist, album string) []spotiflac.MetadataResult {
	if artist == "" && album == "" {
		return results
	}
	filtered := make([]spotiflac.MetadataResult, 0, len(results))
	for _, r := range results {
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
