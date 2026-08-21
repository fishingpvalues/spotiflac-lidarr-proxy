package spotiflac

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// hifiTrackProbeServer serves the version banner on / (like a healthy-looking
// hifi-api) and hands every other path to trackHandler.
func hifiTrackProbeServer(trackHandler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"2.3","Repo":"https://github.com/uimaxbai/hifi-api"}`))
			return
		}
		trackHandler(w, r)
	}))
}

func TestProbeHiFiTrackClassifiesResponses(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantOK  bool
		wantSub string
	}{
		{"manifest served", http.StatusOK, `{"data":{"manifest":"eA=="}}`, true, ""},
		{"queued ticket", http.StatusAccepted, `{"status":"pending","statusUrl":"/playback/requests/1"}`, true, ""},
		{"unknown id but auth works", http.StatusNotFound, `{"detail":"Track not found"}`, true, ""},
		{"oauth token blocked", http.StatusUnauthorized, `{"detail":"Token refresh failed: Client error '403 Forbidden'"}`, false, "Token refresh failed"},
		{"forbidden", http.StatusForbidden, `{"detail":"Upstream API error"}`, false, "HTTP 403"},
		{"upstream 503", http.StatusServiceUnavailable, ``, false, "HTTP 503"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := hifiTrackProbeServer(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			defer srv.Close()

			ok, reason := probeHiFiTrack(srv.URL)
			assert.Equal(t, tc.wantOK, ok, "reason: %s", reason)
			if tc.wantSub != "" {
				assert.Contains(t, reason, tc.wantSub)
			}
		})
	}
}

// The regression this exists for: monochrome-api.samidy.com answered 200 on /
// while every /track/ call failed with "Token refresh failed" (Tidal blocking
// the instance's OAuth credentials, 2026-08-20..21). The old root-only probe
// accepted it, cached it for five minutes, and handed it to spotiflac-cli as
// --tidal-api-url - where every track then failed hard before falling through
// to the community tier. A candidate whose track path is auth-broken must be
// skipped in favor of the next one.
func TestResolveTidalAPISkipsAuthBrokenInstance(t *testing.T) {
	dead := hifiTrackProbeServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Token refresh failed: Client error '403 Forbidden' for url 'https://auth.tidal.com/v1/oauth2/token'"}`))
	})
	defer dead.Close()

	ok := hifiTrackProbeServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // auth works; sentinel unknown
		_, _ = w.Write([]byte(`{"detail":"Track not found"}`))
	})
	defer ok.Close()

	c := NewClient("/usr/bin/spotiflac-cli", 0, "tidal", "lossless", "", dead.URL, "", []string{ok.URL}, "", nil)
	got, kind := c.resolveTidalAPIURL()
	assert.Equal(t, ok.URL, got, "the auth-broken primary must be skipped")
	assert.Equal(t, apiHiFi, kind)

	// Cached verdict: the second call returns the same winner without
	// re-probing (asserted indirectly - it must still be the working one).
	got2, _ := c.resolveTidalAPIURL()
	assert.Equal(t, ok.URL, got2)
}

// With no fallbacks configured the single explicit URL is still verified: an
// auth-broken hifi instance must come back dead rather than being handed to
// the CLI out of deference to the operator's choice.
func TestResolveTidalAPISingleURLAuthBrokenIsDead(t *testing.T) {
	dead := hifiTrackProbeServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"Upstream API error"}`))
	})
	defer dead.Close()

	c := NewClient("/usr/bin/spotiflac-cli", 0, "tidal", "lossless", "", dead.URL, "", nil, "", nil)
	got, kind := c.resolveTidalAPIURL()
	assert.Empty(t, got)
	assert.Equal(t, apiDead, kind)
}
