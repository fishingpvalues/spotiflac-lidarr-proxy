package indexer

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// DefaultExcludePattern matches album titles that are not the studio release
// Lidarr is usually after: live recordings, radio shows and DJ mixes, video
// and lyric-video uploads, karaoke and sped-up/slowed edits, spoken word.
//
// Why it exists, measured 2026-10-16 on one production instance: Lidarr's
// metadata (MusicBrainz plus Deezer via a metadata mix) had 1342 wanted
// albums, 414 of them "Live" and another 143 compilations, and 70 titles
// such as "Hardwell On Air - Best Of May", "Lose Yourself to Dance (Official
// Version)" or "BERSERK -Forces- - TV version". Every one of them was
// searched here, Spotify returned something, and the download then failed
// on every provider: the providers carry the studio catalog, not radio
// shows or video edits, and when they do carry a "live" album it is a
// different edit whose durations fail the CLI's duration check. Those
// releases accounted for most of the failed-download history, retried on
// every Lidarr search cycle.
//
// Anchored on the shapes these titles actually take, never on a bare word:
// "Live" alone would also drop "Live Forever" or "Live and Let Die".
const DefaultExcludePattern = `(?i)` +
	`[(\[]\s*live\b` + // "(Live)", "[Live at ...]"
	`|\blive\s+(?:(?:at|in|from|on|aus|im|op)\b|@)` + // "Live at Wembley", "Live im SO36"
	`|^\s*live\s*$` + // an album titled just "Live"
	`|\s-\s*live\b` + // "Song - Live"
	`|\bunplugged\b` +
	`|\bkaraoke\b` +
	`|\b(official|music|lyrics?)\s+(music\s+)?video\b` +
	`|\bofficial\s+(version|audio|visuali[sz]er)\b` +
	`|\bvisuali[sz]er\b` +
	`|\bsped\s+up\b` +
	`|\bnightcore\b` +
	`|\b8d\s+audio\b` +
	`|\btv\s+(version|size|edit)\b` +
	`|\bon\s+air\b` + // radio shows: "Hardwell On Air", "A State of Trance ... On Air"
	`|\bradio\s+show\b` +
	`|\bepisode\s+\d+` +
	`|\bpodcast\b` +
	`|\binterview\b` +
	`|\bcommentary\b` +
	`|\bmix\s+cut\b` +
	`|\bcontinuous\s+mix\b` +
	`|\bdj\s+mix\b` +
	`|\baudiobook\b` +
	`|\bh(ö|oe)rbuch\b` +
	`|\bh(ö|oe)rspiel\b` +
	`|\bafspeellijst\b` +
	`|\bplaylist\b`

// ExcludeFilter drops search results whose album title marks them as a
// variant release (live, radio show, video, ...) the caller did not ask for.
//
// The rule compares markers, not raw matches: a result is dropped when its
// title carries a marker the requested album does not. So a search for
// "Born to Run" no longer returns "Born to Run (Live)", while a search that
// itself asks for a live album still gets live results - unless SkipQueries
// is set, in which case such a search is not run at all.
type ExcludeFilter struct {
	re *regexp.Regexp
	// SkipQueries answers a search for a variant album with no results
	// instead of searching. That is what stops Lidarr from grabbing a
	// release every provider is going to fail on.
	SkipQueries bool
}

// NewExcludeFilter compiles pattern. An empty pattern or "-" disables the
// filter; the returned filter is then a no-op (and nil-safe).
func NewExcludeFilter(pattern string, skipQueries bool) (*ExcludeFilter, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "-" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("exclude pattern: %w", err)
	}
	return &ExcludeFilter{re: re, SkipQueries: skipQueries}, nil
}

// markers returns the variant markers in s, each reduced to its first word
// ("Live at Wembley" -> "live", "Official Video" -> "official"), so two
// spellings of the same kind of variant compare equal.
func (f *ExcludeFilter) markers(s string) map[string]bool {
	if f == nil || s == "" {
		return nil
	}
	found := f.re.FindAllString(s, -1)
	if len(found) == 0 {
		return nil
	}
	out := make(map[string]bool, len(found))
	for _, m := range found {
		out[firstWord(m)] = true
	}
	return out
}

func firstWord(s string) string {
	s = strings.ToLower(s)
	start := strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if start < 0 {
		return s
	}
	s = s[start:]
	if end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }); end >= 0 {
		s = s[:end]
	}
	return s
}

// wanted is the text that says what the caller asked for: the album when
// Lidarr sent one, else the free-text query.
func wanted(query, album string) string {
	if strings.TrimSpace(album) != "" {
		return album
	}
	return query
}

// SkipQuery reports whether a search should be answered empty without
// running it, and the marker that decided it.
func (f *ExcludeFilter) SkipQuery(query, album string) (bool, string) {
	if f == nil || !f.SkipQueries {
		return false, ""
	}
	for m := range f.markers(wanted(query, album)) {
		return true, m
	}
	return false, ""
}

// Apply removes results that carry a variant marker the request did not.
// It returns the kept results and how many were dropped.
func (f *ExcludeFilter) Apply(results []spotiflac.MetadataResult, query, album string) ([]spotiflac.MetadataResult, int) {
	if f == nil || len(results) == 0 {
		return results, 0
	}
	asked := f.markers(wanted(query, album))
	kept := results[:0:0]
	dropped := 0
	for _, r := range results {
		if f.extra(r.Album, asked) {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	return kept, dropped
}

func (f *ExcludeFilter) extra(title string, asked map[string]bool) bool {
	for m := range f.markers(title) {
		if !asked[m] {
			return true
		}
	}
	return false
}
