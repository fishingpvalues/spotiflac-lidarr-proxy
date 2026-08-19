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

	// An empty query is not an error, it's Lidarr's RSS sync: t=music with
	// no q/artist/album, issued on every indexer refresh. spotiflac-cli
	// rejects --search "" with "error: --url is required" and exit 1, which
	// surfaced as a "newznab music search failed" every ~7 minutes forever.
	// There is nothing to browse here - this indexer only answers directed
	// searches - so return no releases rather than shelling out to fail.
	if strings.TrimSpace(searchQuery) == "" {
		return nil, nil
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
// Three distinct problems observed against production:
//  1. Spotify's search matches query terms anywhere (track title, artist,
//     album), so an artist-only search for e.g. "Lily Phillips" surfaced an
//     unrelated song merely *titled* "Lily Phillips" by a different artist.
//  2. Some track hits have no resolvable containing album (Album == ""),
//     rendering as a "{Artist} - " newznab title that Lidarr's parser
//     rejects outright as unparseable - pure noise at best, and a
//     structurally invalid "release" at worst.
//  3. A track hit and the album hit that contains it looked identical here -
//     same title "{Artist} - {Album} [FLAC]" - but only the album one can
//     actually be imported, because downloading a track URL yields one file
//     and Lidarr needs >= 80% of the album's tracks. Whichever Lidarr picked
//     was luck, and it picked track URLs, so 709 files accumulated on disk
//     with zero imports. Album hits now win their (artist, album) pair;
//     track hits survive only where no album hit covers them, which is what
//     keeps genuine singles searchable.
func filterResults(results []spotiflac.MetadataResult, artist, album string) []spotiflac.MetadataResult {
	matches := func(r spotiflac.MetadataResult) bool {
		if r.Album == "" {
			return false
		}
		if artist != "" && !strings.Contains(strings.ToLower(r.Artist), strings.ToLower(artist)) {
			return false
		}
		if album != "" && !strings.Contains(strings.ToLower(r.Album), strings.ToLower(album)) {
			return false
		}
		return true
	}

	albums := make(map[string]bool)
	for _, r := range results {
		if matches(r) && r.EntityKind() == spotiflac.EntityAlbum {
			albums[releaseKey(r)] = true
		}
	}

	filtered := make([]spotiflac.MetadataResult, 0, len(results))
	for _, r := range results {
		if !matches(r) {
			continue
		}
		if r.EntityKind() != spotiflac.EntityAlbum && albums[releaseKey(r)] {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// releaseKey identifies the release a hit belongs to. Spotify credits a track to
// every featured artist ("Fred again.., Skepta") while crediting the album to the
// primary one, so only the first credit can be compared.
func releaseKey(r spotiflac.MetadataResult) string {
	artist, _, _ := strings.Cut(r.Artist, ",")
	return strings.ToLower(strings.TrimSpace(artist)) + "\x00" + strings.ToLower(strings.TrimSpace(r.Album))
}
