package breaker_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
)

func TestBreakerTripsAfterThreshold(t *testing.T) {
	b := breaker.New(3, 50*time.Millisecond)

	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"))
	b.RecordFailure("tidal")

	assert.False(t, b.Allow("tidal"), "breaker should be open after 3 consecutive failures")
	assert.True(t, b.Allow("qobuz"), "breaker state is per-service")
}

func TestBreakerResetsOnSuccess(t *testing.T) {
	b := breaker.New(3, time.Second)
	b.RecordFailure("tidal")
	b.RecordFailure("tidal")
	b.RecordSuccess("tidal")
	b.RecordFailure("tidal")
	b.RecordFailure("tidal")
	assert.True(t, b.Allow("tidal"), "a success should reset the consecutive-failure count")
}

func TestBreakerClosesAfterCooldown(t *testing.T) {
	b := breaker.New(1, 20*time.Millisecond)
	b.RecordFailure("tidal")
	assert.False(t, b.Allow("tidal"))

	time.Sleep(30 * time.Millisecond)
	assert.True(t, b.Allow("tidal"), "breaker should close again after the cooldown elapses")
}

func TestBreakerStatus(t *testing.T) {
	b := breaker.New(1, time.Minute)
	b.RecordFailure("tidal")
	status := b.Status()
	assert.True(t, status["tidal"].Open)
	assert.False(t, status["qobuz"].Open)
}
