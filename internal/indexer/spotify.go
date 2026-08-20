package indexer

import (
	"context"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

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
	wantArtist := normalizeForMatch(artist)
	wantAlbum := normalizeForMatch(album)

	matches := func(r spotiflac.MetadataResult) bool {
		if r.Album == "" {
			return false
		}
		if wantArtist != "" && !strings.Contains(normalizeForMatch(r.Artist), wantArtist) {
			return false
		}
		if wantAlbum != "" && !strings.Contains(normalizeForMatch(r.Album), wantAlbum) {
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
	return normalizeForMatch(artist) + "\x00" + normalizeForMatch(r.Album)
}

// diacriticFold strips combining marks so "Beyoncé" and "Beyonce" compare
// equal.
var diacriticFold = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// ligatureFold covers the letters Unicode decomposition leaves alone: NFD
// does not turn "æ" into "ae", so "Ágætis byrjun" still failed to match
// "Agaetis byrjun". These are the ones that actually occur in music
// metadata.
var ligatureFold = strings.NewReplacer(
	"æ", "ae", "Æ", "ae",
	"ø", "o", "Ø", "o",
	"œ", "oe", "Œ", "oe",
	"ð", "d", "Ð", "d",
	"þ", "th", "Þ", "th",
	"ß", "ss",
	"đ", "d", "Đ", "d",
	"ł", "l", "Ł", "l",
	"ı", "i",
)

// normalizeForMatch reduces a name to lowercase letters and digits, with
// diacritics folded away.
//
// A plain lowercase strings.Contains was too strict to match what Lidarr
// asks for against what Spotify returns. Lidarr sends the artist and album
// as its own metadata source spells them, and the two disagree constantly on
// punctuation and accents: "Beyoncé" vs "Beyonce", "Motorhead" vs
// "Motörhead", "AC/DC" vs "AC-DC", "Ágætis byrjun" vs "Agaetis byrjun".
// Every one of those filtered the whole result set to nothing, which
// surfaces as an indexer that simply has no releases for that album.
func normalizeForMatch(s string) string {
	folded, _, err := transform.String(diacriticFold, ligatureFold.Replace(s))
	if err != nil {
		folded = s
	}
	var b strings.Builder
	b.Grow(len(folded))
	for _, r := range strings.ToLower(folded) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
