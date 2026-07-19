package verify_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/verify"
)

func TestNotifyPostsMessageAsBodyWithTitleHeader(t *testing.T) {
	var gotBody, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotTitle = r.Header.Get("Title")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := verify.Notify(srv.URL, "SpotiFLAC verification needed", "open this: https://example.com/challenge")
	require.NoError(t, err)

	assert.Equal(t, "open this: https://example.com/challenge", gotBody)
	assert.Equal(t, "SpotiFLAC verification needed", gotTitle)
}

func TestNotifyWithoutTitleOmitsHeader(t *testing.T) {
	var gotTitle string
	seen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle, seen = r.Header.Get("Title"), r.Header.Get("Title") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := verify.Notify(srv.URL, "", "message body")
	require.NoError(t, err)
	assert.False(t, seen, "no Title header should be sent when title is empty, got %q", gotTitle)
}
