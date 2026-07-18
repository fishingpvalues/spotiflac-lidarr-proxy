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
