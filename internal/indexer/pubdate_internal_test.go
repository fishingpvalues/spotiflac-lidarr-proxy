package indexer

import "testing"

func TestStablePubDateIsDeterministicPerRelease(t *testing.T) {
	a := "https://open.spotify.com/album/0YfihR02Ehfw4aAuztOU1t"
	b := "https://open.spotify.com/album/3pMY1xDfAeZ18rRTfSYPfb"
	if !stablePubDate(a).Equal(stablePubDate(a)) {
		t.Fatal("same release must get the same publish date on every search")
	}
	if stablePubDate(a).Equal(stablePubDate(b)) {
		t.Fatal("different releases should not collide on the publish date")
	}
	for _, u := range []string{a, b, ""} {
		d := stablePubDate(u)
		if d.Before(pubDateEpoch) || !d.Before(pubDateEpoch.AddDate(1, 0, 0)) {
			t.Fatalf("publish date %v outside the anchor year", d)
		}
	}
}
