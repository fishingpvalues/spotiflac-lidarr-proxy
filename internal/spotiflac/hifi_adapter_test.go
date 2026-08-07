package spotiflac

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func btsBody(t *testing.T, url string) string {
	t.Helper()
	manifest, err := json.Marshal(hifiManifestBTS{
		MimeType: "audio/flac",
		Codecs:   "flac",
		URLs:     []string{url},
	})
	if err != nil {
		t.Fatal(err)
	}
	return `{"data":{"trackId":1550546,"audioQuality":"LOSSLESS",` +
		`"manifestMimeType":"application/vnd.tidal.bts",` +
		`"manifest":"` + base64.StdEncoding.EncodeToString(manifest) + `"}}`
}

func TestResolveTrackURLDecodesBTSManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(btsBody(t, "https://cdn.example/track.flac")))
	}))
	defer srv.Close()

	got, err := NewHiFiAdapter(srv.URL).ResolveTrackURL("1550546", "LOSSLESS")
	if err != nil {
		t.Fatalf("ResolveTrackURL: %v", err)
	}
	if got.URL != "https://cdn.example/track.flac" {
		t.Fatalf("URL = %q, want the manifest's first URL", got.URL)
	}
}

func TestResolveTrackURLWaitsOutPlaybackQueue(t *testing.T) {
	var mu sync.Mutex
	polls := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/track/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"pending","requestId":"abc",` +
			`"statusUrl":"/playback/requests/abc","queuePosition":2}`))
	})
	mux.HandleFunc("/playback/requests/abc", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		if n < 2 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"processing","requestId":"abc","statusUrl":"/playback/requests/abc"}`))
			return
		}
		_, _ = w.Write([]byte(btsBody(t, "https://cdn.example/queued.flac")))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A busy upstream answers 202 with a queue ticket instead of a manifest.
	// Treating that as "empty manifest" turned ordinary contention into a
	// failed download.
	got, err := NewHiFiAdapter(srv.URL).ResolveTrackURL("1550546", "LOSSLESS")
	if err != nil {
		t.Fatalf("ResolveTrackURL: %v", err)
	}
	if got.URL != "https://cdn.example/queued.flac" {
		t.Fatalf("URL = %q, want the URL from the completed ticket", got.URL)
	}
}

func TestResolveTrackURLReportsUpstreamBlock(t *testing.T) {
	// What monochrome-api.samidy.com actually returns once Tidal has blocked
	// the instance's account. The message has to survive to the log.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"Upstream API error"}`))
	}))
	defer srv.Close()

	_, err := NewHiFiAdapter(srv.URL).ResolveTrackURL("1550546", "LOSSLESS")
	if err == nil {
		t.Fatal("ResolveTrackURL: want error on HTTP 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Upstream API error") {
		t.Fatalf("error = %q, want it to carry the status and upstream detail", err)
	}
}

func TestResolveTrackURLReportsNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><body>Just a moment...</body></html>`))
	}))
	defer srv.Close()

	_, err := NewHiFiAdapter(srv.URL).ResolveTrackURL("1550546", "LOSSLESS")
	if err == nil || !strings.Contains(err.Error(), "body:") {
		t.Fatalf("error = %v, want a decode error quoting the body", err)
	}
}
