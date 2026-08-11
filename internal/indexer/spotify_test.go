package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestFilterResultsDropsTitleOnlyMatches(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "oopsy", Album: "Certified Accident", Title: "Certified Accident"},
		{Artist: "oopsy", Album: "Lily Phillips", Title: "Lily Phillips"},
		{Artist: "Lily Phillips", Album: "Some Album", Title: "Some Track"},
	}

	got := filterResults(results, "Lily Phillips", "")

	assert.Len(t, got, 1, "artist-only search must not match on title/album, only the Artist field")
	assert.Equal(t, "Lily Phillips", got[0].Artist)
}

func TestFilterResultsMatchesArtistCaseInsensitively(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "Daft Punk", Album: "Discovery"},
		{Artist: "daft punk", Album: "Homework"},
		{Artist: "Not Daft Punk", Album: "Other"},
	}

	got := filterResults(results, "DAFT PUNK", "")

	assert.Len(t, got, 3, "\"Not Daft Punk\" contains the query as a substring, matching current contains-based filtering")
}

func TestFilterResultsAppliesAlbumFilterToo(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "Daft Punk", Album: "Discovery"},
		{Artist: "Daft Punk", Album: "Homework"},
	}

	got := filterResults(results, "Daft Punk", "Discovery")

	assert.Len(t, got, 1)
	assert.Equal(t, "Discovery", got[0].Album)
}

func TestFilterResultsPassesThroughWhenNoArtistOrAlbumGiven(t *testing.T) {
	results := []spotiflac.MetadataResult{
		{Artist: "Anything", Album: "Whatever"},
	}

	got := filterResults(results, "", "")

	assert.Equal(t, results, got)
}

func TestFilterResultsDropsResultsWithNoAlbum(t *testing.T) {
	// Reproduces "Bob Sinclar - " showing up in production: a track hit with
	// no resolvable containing album renders as an unparseable newznab
	// title and can never be grabbed as an album release by Lidarr.
	results := []spotiflac.MetadataResult{
		{Artist: "Bob Sinclar", Album: "", Title: "I Feel for You"},
		{Artist: "Bob Sinclar", Album: "My Love", Title: "My Love"},
	}

	got := filterResults(results, "Bob Sinclar", "")

	assert.Len(t, got, 1)
	assert.Equal(t, "My Love", got[0].Album)
}

func TestFilterResultsPrefersAlbumOverItsOwnTracks(t *testing.T) {
	// Downloading a track URL yields one file, and Lidarr imports nothing
	// below an 80% album match, so the album hit must be the release we
	// publish for a given (artist, album).
	results := []spotiflac.MetadataResult{
		{Artist: "Eminem", Album: "Kamikaze", Title: "The Ringer", Entity: "track",
			SpotifyURL: "https://open.spotify.com/track/2jt2WxXMCD4zjACthkJQVE"},
		{Artist: "Eminem", Album: "Kamikaze", Title: "Kamikaze", Entity: "album",
			SpotifyURL: "https://open.spotify.com/album/3HNnxK7NgLXbDoxRZxNWiR"},
		{Artist: "Eminem", Album: "Kamikaze", Title: "Fall", Entity: "track",
			SpotifyURL: "https://open.spotify.com/track/58QhkbaAkLFnn7JwAnAato"},
	}

	got := filterResults(results, "Eminem", "")

	assert.Len(t, got, 1)
	assert.Contains(t, got[0].SpotifyURL, "/album/")
}

func TestFilterResultsKeepsTracksWithNoAlbumCounterpart(t *testing.T) {
	// A single has no album hit, and dropping it would make singles - the bulk
	// of what this indexer is asked for - unsearchable.
	results := []spotiflac.MetadataResult{
		{Artist: "Fred again..", Album: "solo", Title: "solo", Entity: "track",
			SpotifyURL: "https://open.spotify.com/track/6U5h4WhbYufaRGXQhnileY"},
	}

	got := filterResults(results, "", "")

	assert.Len(t, got, 1)
	assert.Contains(t, got[0].SpotifyURL, "/track/")
}

func TestFilterResultsComparesOnlyThePrimaryArtist(t *testing.T) {
	// Spotify credits a track to every featured artist but the album to the
	// primary one, so a naive full-string compare never matches the pair up.
	results := []spotiflac.MetadataResult{
		{Artist: "Eminem, Joyner Lucas", Album: "Kamikaze", Title: "Lucky You", Entity: "track",
			SpotifyURL: "https://open.spotify.com/track/60SdxE8apGAxMiRrpbmLY0"},
		{Artist: "Eminem", Album: "Kamikaze", Title: "Kamikaze", Entity: "album",
			SpotifyURL: "https://open.spotify.com/album/3HNnxK7NgLXbDoxRZxNWiR"},
	}

	got := filterResults(results, "", "")

	assert.Len(t, got, 1)
	assert.Contains(t, got[0].SpotifyURL, "/album/")
}

func TestFilterResultsDerivesEntityFromURLWhenFieldMissing(t *testing.T) {
	// CLI builds older than the `entity` field emit neither it nor an album
	// name for album hits; the URL path is the fallback.
	results := []spotiflac.MetadataResult{
		{Artist: "Eminem", Album: "Kamikaze", Title: "Fall",
			SpotifyURL: "https://open.spotify.com/track/58QhkbaAkLFnn7JwAnAato"},
		{Artist: "Eminem", Album: "Kamikaze", Title: "Kamikaze",
			SpotifyURL: "https://open.spotify.com/album/3HNnxK7NgLXbDoxRZxNWiR"},
	}

	got := filterResults(results, "", "")

	assert.Len(t, got, 1)
	assert.Contains(t, got[0].SpotifyURL, "/album/")
}
