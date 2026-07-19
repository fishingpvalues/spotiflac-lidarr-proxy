package verify_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/verify"
)

func newTestApp(store *verify.Store) *fiber.App {
	app := fiber.New()
	verify.NewHandler(store).RegisterRoutes(app)
	return app
}

func TestCallbackRelaysGrantToExpectedListener(t *testing.T) {
	var gotState, gotGrant string
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotState = r.URL.Query().Get("state")
		gotGrant = r.URL.Query().Get("grant")
		_, _ = w.Write([]byte("verified"))
	}))
	defer loopback.Close()

	expectedCB := loopback.URL + "/session-grant?state=abc123"
	store := verify.NewStore()
	store.Set("https://verify.example/challenge?cb="+url.QueryEscape(expectedCB), expectedCB)
	app := newTestApp(store)

	req, _ := http.NewRequest("GET", "/verify/callback?upstream_cb="+
		url.QueryEscape(expectedCB)+"&grant=grantvalue", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	assert.Equal(t, "abc123", gotState, "the loopback listener's own state param must survive the relay unchanged")
	assert.Equal(t, "grantvalue", gotGrant)

	_, _, pending := store.Pending()
	assert.False(t, pending, "a completed relay must clear the pending-verification store")
}

func TestCallbackRejectsUpstreamCBNotMatchingPending(t *testing.T) {
	// Regression guard: "is it a loopback address" alone isn't enough - any
	// caller could still make this proxy issue a request to an arbitrary
	// port/path on its own loopback (e.g. other services sharing a network
	// namespace in production, such as the gluetun sidecar). The relay must
	// only ever forward to the exact upstream_cb spotiflac-cli itself
	// reported expecting one for.
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the loopback listener must never be hit for a mismatched upstream_cb")
	}))
	defer loopback.Close()

	store := verify.NewStore()
	store.Set("https://verify.example/challenge?cb=x", loopback.URL+"/session-grant?state=real")

	app := newTestApp(store)
	req, _ := http.NewRequest("GET", "/verify/callback?upstream_cb="+
		url.QueryEscape(loopback.URL+"/session-grant?state=attacker-supplied")+
		"&grant=grantvalue", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}

func TestCallbackRejectsWhenNothingPending(t *testing.T) {
	app := newTestApp(verify.NewStore())

	req, _ := http.NewRequest("GET", "/verify/callback?upstream_cb="+
		url.QueryEscape("http://127.0.0.1:9999/session-grant?state=x")+
		"&grant=grantvalue", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 410, resp.StatusCode)
}

func TestCallbackRequiresBothParams(t *testing.T) {
	app := newTestApp(verify.NewStore())

	req, _ := http.NewRequest("GET", "/verify/callback?grant=onlygrant", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}
