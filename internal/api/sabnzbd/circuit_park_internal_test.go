package sabnzbd

import (
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// noPythonHandler builds a Handler shaped like the production container:
// spotiflac-cli present, no Python backend, and the deployed fallback chain
// "qobuz,deezer,amazon".
func noPythonHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := &config.Config{
		DefaultService:   config.ServiceTidal,
		FallbackServices: []string{config.ServiceQobuz, config.ServiceDeezer, config.ServiceAmazon},
		MaxConcurrent:    1,
	}
	// A venv path that does not exist is what HasPythonBackend() sees in the
	// shipped image (/venv/bin/python3 is a bare interpreter with no
	// spotiflac module); either way SupportsService must refuse deezer.
	client := spotiflac.NewClient(
		"/nonexistent/spotiflac-cli", time.Minute,
		config.ServiceTidal, "lossless", "", "", "", nil,
		t.TempDir()+"/no-such-python", cfg.FallbackServices,
	)
	h := NewHandler(nil, client, nil, cfg, "test")
	h.log = zerolog.Nop()
	return h
}

func TestFallbackChainDropsUnsupportedService(t *testing.T) {
	h := noPythonHandler(t)
	if h.client.HasPythonBackend() {
		t.Skip("python backend present in this environment; the filter is a no-op here")
	}

	chain := h.fallbackChain(config.ServiceTidal)
	for _, svc := range chain {
		if svc == config.ServiceDeezer {
			t.Fatalf("deezer must not appear in the fallback chain without a Python backend, got %v", chain)
		}
	}
	want := []string{config.ServiceQobuz, config.ServiceAmazon}
	if len(chain) != len(want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Fatalf("chain = %v, want %v (configured order must survive the filter)", chain, want)
		}
	}
}

func TestCandidateServicesSkipsUnsupportedPrimary(t *testing.T) {
	h := noPythonHandler(t)
	if h.client.HasPythonBackend() {
		t.Skip("python backend present in this environment")
	}

	got := h.candidateServices(&queue.Job{Service: config.ServiceDeezer})
	for _, svc := range got {
		if svc == config.ServiceDeezer {
			t.Fatalf("unsupported primary must not be a candidate, got %v", got)
		}
	}
	if len(got) == 0 {
		t.Fatal("a deezer job must still have qobuz/amazon candidates")
	}
}

func TestParkForOpenCircuitsReturnsWhenAnyServiceAllowed(t *testing.T) {
	h := noPythonHandler(t)
	// tidal open, qobuz/amazon closed: one healthy candidate is enough.
	for i := 0; i < 5; i++ {
		h.breaker.RecordFailure(config.ServiceTidal)
	}

	done := make(chan struct{})
	go func() {
		h.parkForOpenCircuits(&queue.Job{NzoID: "x", Service: config.ServiceTidal})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("park must return immediately while a fallback service is still allowed")
	}
}

// The regression this whole change exists for: with every candidate breaker
// open the job used to fall through to failJob with "circuit open" as its
// entire error. It must park instead, and resume once the cooldown lifts.
func TestParkForOpenCircuitsWaitsUntilCooldownLifts(t *testing.T) {
	h := noPythonHandler(t)
	h.breaker = breaker.New(1, 300*time.Millisecond)
	prevPoll := circuitParkPoll
	circuitParkPoll = 20 * time.Millisecond
	defer func() { circuitParkPoll = prevPoll }()

	for _, svc := range []string{config.ServiceTidal, config.ServiceQobuz, config.ServiceAmazon} {
		h.breaker.RecordFailure(svc)
	}
	if h.breaker.RetryAfter(config.ServiceTidal) <= 0 {
		t.Fatal("precondition: tidal breaker must be open")
	}

	start := time.Now()
	h.parkForOpenCircuits(&queue.Job{NzoID: "x", Service: config.ServiceTidal})
	elapsed := time.Since(start)

	if elapsed < 250*time.Millisecond {
		t.Fatalf("park returned after %s; it must wait out the cooldown instead of failing the job", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("park took %s; it must return promptly once a breaker closes", elapsed)
	}
}

func TestParkForOpenCircuitsHonorsCap(t *testing.T) {
	h := noPythonHandler(t)
	h.breaker = breaker.New(1, time.Hour)
	prevPoll := circuitParkPoll
	circuitParkPoll = 10 * time.Millisecond
	defer func() { circuitParkPoll = prevPoll }()
	for _, svc := range []string{config.ServiceTidal, config.ServiceQobuz, config.ServiceAmazon} {
		h.breaker.RecordFailure(svc)
	}

	// A zero cap makes the loop take its give-up branch on the first pass
	// rather than sleeping out the hour-long cooldown.
	prevCap := maxCircuitPark
	maxCircuitPark = 0
	defer func() { maxCircuitPark = prevCap }()

	done := make(chan struct{})
	go func() {
		h.parkForOpenCircuits(&queue.Job{NzoID: "x", Service: config.ServiceTidal})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("park must give up at the cap instead of holding the job forever")
	}
}
