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
