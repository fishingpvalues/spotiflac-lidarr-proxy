# Python Wrapper Integration Audit + E2E Cascade Tests

## Summary

Verify SpotiFLAC Python wrapper integration, write cascade fallback E2E tests, fix
hardcoded service list, remove duplicate script, push and fix CI.

## Changes

### 1. Fix hardcoded service cascade

`downloadWithPython` hardcodes `--service <primary>,qobuz,deezer,amazon`.
Replace with configured `SPF_FALLBACK_SERVICES` appended after primary service.

**File:** `internal/spotiflac/client.go`

### 2. Remove duplicate Python wrapper

`scripts/spotiflac-py-wrapper.py` is a stale copy of the embedded wrapper.
Delete it.

**File:** `scripts/spotiflac-py-wrapper.py` — delete

### 3. E2E cascade tests (mock-based, no real downloads)

New test file: `internal/spotiflac/client_cascade_test.go`

Tests:
- **Python wrapper succeeds** — emits "complete", CLI never invoked
- **Python wrapper fails** — no "complete" event, falls through to CLI
- **Python wrapper not available** — no python binary, skips to CLI
- **collectPythonResult** — only forwards events after "complete"
- **Full cascade order** — Python first, then CLI primary with retries, then fallback chain

Existing tests in `handler_test.go` already cover:
- Retry behavior (3 attempts, backoff)
- Fallback chain (primary -> fallback services)
- Circuit breaker (open after 5 failures, short-circuits)
- Stale file cleanup between retries and across fallback transitions

### 4. Run tests, lint, build, push

```
make test-race
make lint
make build
git commit && git push
```

Monitor CI pipeline, fix if anything breaks.
