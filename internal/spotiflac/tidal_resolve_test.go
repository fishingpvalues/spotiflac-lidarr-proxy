package spotiflac

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func hifiRoot(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.3","Repo":"https://github.com/uimaxbai/hifi-api"}`))
	}))
}

func statusRoot(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
}

func TestProbeAPIRejectsHTMLWebUI(t *testing.T) {
	// lossless.wtf and monochrome.samidy.com are the Monochrome web UI:
	// HTTP 200, but HTML. Accepting them hands spotiflac-cli a page instead
	// of an API.
	srv := statusRoot(t, http.StatusOK, `<!doctype html><html lang="en"><head>`)
	defer srv.Close()

	if got := probeAPI(srv.URL); got != apiDead {
		t.Fatalf("probeAPI(html 200) = %v, want apiDead", got)
	}
}

func TestProbeAPIRejectsUnhealthyStatus(t *testing.T) {
	for _, code := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusForbidden} {
		srv := statusRoot(t, code, `{"detail":"nope"}`)
		if got := probeAPI(srv.URL); got != apiDead {
			t.Errorf("probeAPI(HTTP %d) = %v, want apiDead", code, got)
		}
		srv.Close()
	}
}

func TestProbeAPIDetectsHiFiAndPlainAPI(t *testing.T) {
	hifi := hifiRoot(t)
	defer hifi.Close()
	if got := probeAPI(hifi.URL); got != apiHiFi {
		t.Fatalf("probeAPI(hifi-api) = %v, want apiHiFi", got)
	}

	plain := statusRoot(t, http.StatusOK, `{"ok":true}`)
	defer plain.Close()
	if got := probeAPI(plain.URL); got != apiSpotiFLAC {
		t.Fatalf("probeAPI(json 200) = %v, want apiSpotiFLAC", got)
	}
}

func TestResolveTidalAPIURLSkipsDeadPrimary(t *testing.T) {
	dead := statusRoot(t, http.StatusServiceUnavailable, "down")
	defer dead.Close()
	good := hifiRoot(t)
	defer good.Close()

	c := NewClient("", time.Minute, "tidal", "lossless", "", dead.URL, "",
		[]string{dead.URL, good.URL}, "", nil)

	url, kind := c.resolveTidalAPIURL()
	if url != good.URL {
		t.Fatalf("resolveTidalAPIURL() = %q, want the healthy fallback %q", url, good.URL)
	}
	if kind != apiHiFi {
		t.Fatalf("kind = %v, want apiHiFi", kind)
	}
}

func TestResolveTidalAPIURLReturnsEmptyWhenAllDead(t *testing.T) {
	dead := statusRoot(t, http.StatusBadGateway, "down")
	defer dead.Close()

	c := NewClient("", time.Minute, "tidal", "lossless", "", dead.URL, "",
		[]string{dead.URL}, "", nil)

	// Returning the dead primary anyway is worse than returning nothing:
	// with no --tidal-api-url, SpotiFLAC falls through to its other backends.
	if url, kind := c.resolveTidalAPIURL(); url != "" || kind != apiDead {
		t.Fatalf("resolveTidalAPIURL() = (%q, %v), want (\"\", apiDead)", url, kind)
	}
}

func TestResolveTidalAPIURLCachesFailure(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()

	c := NewClient("", time.Minute, "tidal", "lossless", "", "", "",
		[]string{dead.URL}, "", nil)

	for i := 0; i < 5; i++ {
		c.resolveTidalAPIURL()
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("probed dead candidate %d times, want 1 (negative result must be cached)", hits)
	}
}

func TestResolveTidalAPIURLIsRaceFree(t *testing.T) {
	good := hifiRoot(t)
	defer good.Close()

	c := NewClient("", time.Minute, "tidal", "lossless", "", "", "",
		[]string{good.URL}, "", nil)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.resolveTidalAPIURL()
		}()
	}
	wg.Wait()
}
