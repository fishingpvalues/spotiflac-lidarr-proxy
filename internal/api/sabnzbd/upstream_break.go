package sabnzbd

import (
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
}

// breakPattern matches the "try again in about N minute(s)" clause. The
// prefix varies between providers ("taking a scheduled short break",
// "overloaded and taking a short break"), so anchor on the stable part.
var breakPattern = regexp.MustCompile(`(?i)short break[^\d]{0,80}(\d+)\s*minute`)

func newUpstreamBreakGate(log zerolog.Logger) *upstreamBreakGate {
	return &upstreamBreakGate{now: time.Now, poll: 15 * time.Second, log: log}
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
	m := breakPattern.FindAllStringSubmatch(err, -1)
	if len(m) == 0 {
		return false
	}
	var best time.Duration
	for _, mm := range m {
		mins, perr := strconv.Atoi(mm[1])
		if perr != nil || mins <= 0 {
			continue
		}
		if d := time.Duration(mins) * time.Minute; d > best {
			best = d
		}
	}
	if best == 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cand := g.now().Add(best)
	if cand.After(g.until) {
		g.until = cand
		g.log.Warn().Dur("break_duration", best).Time("until", cand).
			Msg("upstream community API announced a scheduled break; pausing queue")
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
