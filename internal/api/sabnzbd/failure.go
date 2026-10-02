package sabnzbd

import (
	"regexp"
	"time"
)

// failureClass is what a backend error means for the job that produced it.
//
// Before this existed every error was treated the same way: retry it three
// times, try every fallback service, and record a breaker failure for each
// service. That is right for a service-side failure and wrong for the other
// two classes, both of which were measured in production:
//
//   - An upstream-imposed cooldown ("the server is taking a scheduled short
//     break") is upstream telling us to STOP. Retrying it burns the single
//     concurrency slot, and the failures it records open every service's
//     breaker, which then parks the whole queue behind a 10-minute cooldown
//     that upstream never asked for. Worse, the gate that exists to park the
//     queue looked only at the LAST error of the chain, so a chain whose
//     first two services announced a break and whose third failed with an
//     unrelated Amazon 404 never parked anything: measured 2026-10-01
//     05:41-06:36, a 79-minute announced break drained 18 jobs into Lidarr's
//     failed history, all of them with "circuit open" as the message.
//
//   - A release-specific resolution failure ("songlink/songstats couldn't
//     find Tidal URL", "Amazon API returned status 404") will produce the
//     same answer on every retry, forever. Retrying it spends the slot three
//     times over, and, being repeated across enough releases, it opens the
//     breakers that unrelated releases are then parked behind.
type failureClass int

const (
	// failureRetryable is a service-side failure: try again, try the other
	// services, and let the breaker see it.
	failureRetryable failureClass = iota
	// failureUpstreamCooldown is upstream asking for a pause. The break gate
	// owns the response; nothing else should react.
	failureUpstreamCooldown
	// failurePermanent is this release, not this service. One attempt per
	// service is enough, and it must not open a breaker that other releases
	// are then held behind.
	failurePermanent
)

// permanentFailurePattern matches the errors that are a property of the
// release (or of the metadata graph) rather than of the service. Anchored on
// strings the backends actually emit, not on HTTP status codes in general: a
// bare "404" or "not found" would also match transient mirror failures that
// are worth retrying.
var permanentFailurePattern = regexp.MustCompile(`(?i)` +
	`couldn't find (tidal|qobuz|amazon|deezer) url` +
	`|no platform links found` +
	`|track link not found for isrc` +
	`|(amazon|tidal|qobuz|deezer) api returned status 40[034]` +
	`|unknown service:` +
	`|is only available through the python backend` +
	`|is not available in this deployment` +
	`|no results found in the configured categories` +
	`|spotify (track|album|playlist) not found` +
	`|invalid spotify (url|uri|id)`,
)

// classifyFailure sorts one attempt's final error into a failureClass, and
// for a cooldown reports how long upstream asked us to stay away.
//
// The cooldown duration comes from the same parser the break gate uses, so
// there is exactly one definition of "upstream announced a break" in the
// codebase.
func (h *Handler) classifyFailure(errMsg string) (failureClass, time.Duration) {
	if d, ok := h.breakGate.cooldownFor(errMsg); ok {
		return failureUpstreamCooldown, d
	}
	if permanentFailurePattern.MatchString(errMsg) {
		return failurePermanent, 0
	}
	return failureRetryable, 0
}

// jobOutcome accumulates what happened across one job's whole attempt chain
// (primary retries plus every fallback service) so the terminal decision can
// be made once, from all of it, instead of from the last error alone.
type jobOutcome struct {
	// attempted is true as soon as one backend invocation actually ran. When
	// it is false at the end, the job was never tried: every candidate
	// service was skipped because its circuit was open. That is not a
	// download result and must never be reported as one.
	attempted bool
	// cooldown is set when any service in the chain announced a break, and
	// holds the longest duration announced.
	cooldown time.Duration
	// permanent is set when the last attempted service failed for a reason
	// that is a property of the release.
	permanent bool
	// services lists, in order, the services that were actually attempted.
	services []string
}

// observe folds one attempt error into the outcome and reports whether the
// chain should stop walking because of it. A cooldown stops it: upstream has
// said wait, and every further request is exactly the hammering the
// announcement is asking us to stop.
//
// The break gate is extended here, at the moment the announcement is seen,
// NOT at the end of the chain. That is what makes the pause effective for
// the rest of the queue: the gate is shared, so every other parked job
// notices immediately, and a chain whose later service fails for an
// unrelated reason can no longer hide the announcement.
//
// attempted says whether this particular step really invoked a backend. A
// step that returned "canceled" before running anything has learned nothing
// and is not folded in.
func (h *Handler) observe(errMsg string, attempted bool, out *jobOutcome, service string) (stop bool) {
	if !attempted {
		return false
	}
	out.attempted = true
	out.services = append(out.services, service)

	class, cooldown := h.classifyFailure(errMsg)
	switch class {
	case failureUpstreamCooldown:
		h.breakGate.extend(cooldown)
		if cooldown > out.cooldown {
			out.cooldown = cooldown
		}
		out.permanent = false
		h.log.Warn().Str("service", service).Dur("cooldown", cooldown).
			Msg("upstream asked for a pause; stopping this job's attempt chain")
		return true
	case failurePermanent:
		out.permanent = true
		h.log.Info().Str("service", service).
			Msg("release-specific failure; not retrying it and not recording a service failure")
		return false
	default:
		out.permanent = false
		return false
	}
}

// shouldRetry reports whether another attempt of the SAME service is worth
// making. A cooldown is upstream's answer to the whole queue, and a
// permanent failure is upstream's answer about this release; neither changes
// by asking again three seconds later.
func (h *Handler) shouldRetry(errMsg string) bool {
	class, _ := h.classifyFailure(errMsg)
	return class == failureRetryable
}

// recordServiceFailure feeds the circuit breaker for a real service-side
// failure only.
//
// Budget/cancel deaths were already excluded (a dead context is not a
// service signal); cooldowns and release-specific failures are excluded for
// the same reason. Measured consequence of NOT excluding them, 2026-10-01
// 05:41-06:36: one announced 79-minute break plus a handful of unresolvable
// releases opened all three service breakers, and the 18 jobs behind them
// were failed with "circuit open" without a single attempt.
func (h *Handler) recordServiceFailure(service string, class failureClass) {
	if class != failureRetryable {
		return
	}
	h.breaker.RecordFailure(service)
}
