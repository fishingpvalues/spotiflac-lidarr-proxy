package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"
)

func TestMetricsHandlerExposesJobCounter(t *testing.T) {
	metrics.RecordJobResult("Completed", "tidal")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.PromHTTPHandler().ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `spf_jobs_total{service="tidal",status="Completed"}`)
}

func TestSetQueueDepth(t *testing.T) {
	metrics.SetQueueDepth("Queued", 5)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.PromHTTPHandler().ServeHTTP(rec, req)

	assert.True(t, strings.Contains(rec.Body.String(), `spf_queue_depth{status="Queued"} 5`))
}
