package config

import "regexp"

// spotifyURLPattern is a strict allowlist for well-formed Spotify track,
// album, and playlist links. It is fully anchored (^...$) so no prefix or
// suffix content can smuggle something else through: the scheme and host
// are literal (no room for userinfo/host-confusion tricks like
// "open.spotify.com@evil.com" or "open.spotify.com.evil.com", since the
// character immediately following the literal host must be "/"), the
// optional locale segment is a tightly bounded "intl-xx" or "intl-xx-XX"
// token, the resource type is restricted to track|album|playlist (no
// "artist", no arbitrary path), and the resource ID is restricted to
// alphanumerics only — which also rules out path traversal sequences like
// "../" or percent-encoded characters. An optional query string is allowed
// after a literal "?", but Go's RE2 "$" anchors to the true end of the
// input (not before a trailing newline) unless the "m" flag is set, which
// it is not here, so trailing garbage cannot be smuggled in via a newline
// either.
var spotifyURLPattern = regexp.MustCompile(`^https://open\.spotify\.com/(intl-[a-z]{2}(-[A-Z]{2})?/)?(track|album|playlist)/[A-Za-z0-9]+(\?.*)?$`)

// IsValidSpotifyURL reports whether url is a well-formed Spotify track,
// album, or playlist link. This is enforced before the URL is ever passed
// as a CLI argument to spotiflac-cli, closing the argument-injection vector
// where an arbitrary string (e.g. "--output-dir") could otherwise reach the
// subprocess's argv as the --url value.
func IsValidSpotifyURL(url string) bool {
	return spotifyURLPattern.MatchString(url)
}
