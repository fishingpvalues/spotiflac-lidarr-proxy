package sabnzbd

import (
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// The spotbye community infrastructure (shared by the tidal/qobuz/deezer/
// amazon CLI backends) imposes GLOBAL scheduled cooldowns under load and
// answers every request during the window with a message like:
//
//	The server is taking a scheduled short break. Please try again in about 104 minute(s).
//	The server is overloaded and taking a short break. Please try again in about 104 minute(s), thanks for your patience.
//
// Observed 2026-08-22: a ~47-job burst (outage backlog drain + DAGU re-grab)
// triggered a 104-minute break. Without special handling the queue kept
// walking every job through the full 4-service fallback chain against the
// dead infra (~90 s of requests per job, all failing), marking the whole
// backlog Failed in Lidarr's history and depending on external re-grab
// cycles to retry. The gate below turns that into a pause: the first job
// that hits the message parks the queue until the stated time, jobs stay
// Queued (honest: they ARE queued), zero requests hit the infra during the
// window, and the backlog drains serially when the break lifts.
type upstreamBreakGate struct {
	mu    sync.Mutex
	until time.Time
	now   func() time.Time
	poll  time.Duration
	log   zerolog.Logger

	// rateLimitPark is how long a 429 from the community API parks the
	// queue. A 429 carries no "try again in N minutes" clause, so the
	// duration is ours to pick; see rateLimitPattern.
	rateLimitPark time.Duration
}

// breakPattern matches the "try again in about N minute(s)" clause. The
// prefix varies between providers ("taking a scheduled short break",
// "overloaded and taking a short break"), so anchor on the stable part.
var breakPattern = regexp.MustCompile(`(?i)short break[^\d]{0,80}(\d+)\s*minute`)

// rateLimitPattern matches the community API's rate-limit refusal, which is
// the same class of signal as a scheduled break - upstream telling us to
// stop - but carries no duration:
//
//	Tidal community request failed: Tidal community API rate limited (429)
//	track X: Download failed: all requested Tidal qualities failed: failed to get download URL: Tidal community API rate limited (429)
//
// Without this the queue kept hammering: measured 2026-08-22 during the
// 177-album backlog drain, one job held the single concurrency slot for
// ~25 min retrying into 429s while six more queued behind it, and the drain
// flatlined at 173 missing albums for a quarter of an hour.
var rateLimitPattern = regexp.MustCompile(`(?i)rate limited \(429\)|\b429\b[^\n]{0,40}too many requests|too many requests`)

// defaultRateLimitPark is deliberately short. A 429 is a "slow down", not a
// maintenance window: park long enough that the next job is not part of the
// same burst, short enough that a transient limit does not idle the queue.
const defaultRateLimitPark = 90 * time.Second

// rateLimitParkFromEnv reads SPF_RATE_LIMIT_PARK_S, falling back to
// defaultRateLimitPark for unset, unparseable and non-positive values.
func rateLimitParkFromEnv() time.Duration {
	raw := os.Getenv("SPF_RATE_LIMIT_PARK_S")
	if raw == "" {
		return defaultRateLimitPark
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return defaultRateLimitPark
	}
	return time.Duration(secs) * time.Second
}

func newUpstreamBreakGate(log zerolog.Logger) *upstreamBreakGate {
	return &upstreamBreakGate{
		now:           time.Now,
		poll:          15 * time.Second,
		log:           log,
		rateLimitPark: rateLimitParkFromEnv(),
	}
}

// setLogger re-points the logger after construction (the handler's real
// logger is wired post-construction via SetLogger).
func (g *upstreamBreakGate) setLogger(log zerolog.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log = log
}

// record inspects a final job error and extends the break window if it
// carries a scheduled-break message. Monotonic: a shorter window never
// shrinks a longer one. Returns true when the window changed.
func (g *upstreamBreakGate) record(err string) bool {
	d, ok := g.cooldownFor(err)
	if !ok {
		return false
	}
	return g.extend(d)
}

// cooldownFor classifies a final job error as an upstream-imposed cooldown
// and reports how long to stay off the infra. Two shapes, in priority order:
// an announced scheduled break (authoritative duration, longest match wins)
// and a rate-limit refusal (no duration; rateLimitPark applies).
//
// Split out of record so the caller can tell "upstream said wait" from "this
// release is broken" WITHOUT extending the window twice - the first is worth
// requeuing the job for, the second is a genuine failure.
func (g *upstreamBreakGate) cooldownFor(err string) (time.Duration, bool) {
	var best time.Duration
	for _, mm := range breakPattern.FindAllStringSubmatch(err, -1) {
		mins, perr := strconv.Atoi(mm[1])
		if perr != nil || mins <= 0 {
			continue
		}
		if d := time.Duration(mins) * time.Minute; d > best {
			best = d
		}
	}
	if best > 0 {
		return best, true
	}
	if rateLimitPattern.MatchString(err) {
		g.mu.Lock()
		park := g.rateLimitPark
		g.mu.Unlock()
		if park <= 0 {
			park = defaultRateLimitPark
		}
		return park, true
	}
	return 0, false
}

// extend pushes the park window out to now+d. Monotonic: a shorter window
// never shrinks a longer one. Returns true when the window actually moved.
func (g *upstreamBreakGate) extend(d time.Duration) bool {
	if d <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cand := g.now().Add(d)
	if cand.After(g.until) {
		g.until = cand
		g.log.Warn().Dur("break_duration", d).Time("until", cand).
			Msg("upstream community API asked us to back off; pausing queue")
		return true
	}
	return false
}

// wait blocks until the break window has passed. Called BEFORE the
// concurrency semaphore is taken, so a parked job holds no slot - with
// SPF_MAX_CONCURRENT=1 a wait inside the semaphore would wedge the whole
// queue for the break duration. Polls every 15 s so a concurrently
// extended window (another job parsing a longer break) is honored without
// restarting the waiter.
func (g *upstreamBreakGate) wait() {
	for {
		g.mu.Lock()
		remaining := g.until.Sub(g.now())
		g.mu.Unlock()
		if remaining <= 0 {
			return
		}
		step := g.poll
		if remaining < step {
			step = remaining
		}
		time.Sleep(step)
	}
}

// remaining reports how long the queue is paused (0 when not). Exposed for
// mode=warnings visibility.
func (g *upstreamBreakGate) remaining() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r := g.until.Sub(g.now()); r > 0 {
		return r
	}
	return 0
}

// summarize renders the gate state for operator-facing endpoints.
func (g *upstreamBreakGate) summarize() string {
	if r := g.remaining(); r > 0 {
		return "upstream community break active, queue paused for " + r.Round(time.Second).String()
	}
	return ""
}
