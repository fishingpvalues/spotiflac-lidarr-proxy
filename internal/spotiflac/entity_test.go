package spotiflac_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func TestSearchMetadataRecoversAlbumNameFromOldCLIOutput(t *testing.T) {
	// A CLI without the entity/album fix emits album hits with "album": "".
	// The indexer requires an album name, so without this recovery every album
	// - the only thing Lidarr can import - was discarded.
	m := spotiflac.MetadataResult{
		Title:      "Kamikaze",
		Artist:     "Eminem",
		SpotifyURL: "https://open.spotify.com/album/3HNnxK7NgLXbDoxRZxNWiR",
	}
	assert.Equal(t, spotiflac.EntityAlbum, m.EntityKind())

	track := spotiflac.MetadataResult{SpotifyURL: "https://open.spotify.com/track/58QhkbaAkLFnn7JwAnAato"}
	assert.Equal(t, spotiflac.EntityTrack, track.EntityKind())

	explicit := spotiflac.MetadataResult{Entity: spotiflac.EntityAlbum, SpotifyURL: "https://open.spotify.com/track/x"}
	assert.Equal(t, spotiflac.EntityAlbum, explicit.EntityKind(), "the CLI's own entity field wins over the URL path")

	assert.Empty(t, spotiflac.MetadataResult{SpotifyURL: "https://open.spotify.com/playlist/x"}.EntityKind())
}
