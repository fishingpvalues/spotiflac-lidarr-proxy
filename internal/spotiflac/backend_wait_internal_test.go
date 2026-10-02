package spotiflac

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A backend subprocess whose stdout pipe is held open by a surviving
// grandchild must not pin the job.
//
// exec.CommandContext kills only the direct child, and SpotiFLAC leaves its
// Chromium and node bridge running, so reading to EOF and only then calling
// Wait can block forever: the job stays "Downloading", holds the only
// concurrency slot, and nothing the job's budget can cancel is still alive.
// Measured on potatostack, the external monitor read that as "slots in the
// queue, 0 B/s" and restarted the container eight times in a week.
func TestStreamBackendReturnsWhenAGrandchildHoldsThePipe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs /bin/sh")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// The shell exits immediately, but it leaves a background child that
	// inherits stdout and sleeps far longer than the test.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & exit 0")
	cmd.SysProcAttr = processGroupAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	events := make(chan ProgressEvent, 8)
	sink := newErrSink(make(chan error, 1))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = streamBackend(ctx, cmd, stdout, events, sink.send, &bytes.Buffer{}, nil)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("streamBackend never returned: a grandchild holding the stdout pipe pinned the job")
	}
}

// The read has to end within the drain grace even when the CONTEXT is what
// ended the run, not the process.
func TestStreamBackendEndsOnContextCancel(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs /bin/sh")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = processGroupAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	events := make(chan ProgressEvent, 8)
	sink := newErrSink(make(chan error, 1))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = streamBackend(ctx, cmd, stdout, events, sink.send, &bytes.Buffer{}, nil)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("a cancelled job must return promptly, not wait for its backend to notice")
	}
}

// Only the first terminal error of a run is ever consumed, because the
// handler returns as soon as it has one. Sending a second one into the small
// buffered channel blocked the producer goroutine forever - one leaked
// goroutine, and a channel pair that was never closed, per failed attempt.
func TestErrSinkKeepsTheFirstErrorAndNeverBlocks(t *testing.T) {
	ch := make(chan error, 1)
	sink := newErrSink(ch)

	first := errors.New("first")
	sink.send(first)
	// Nobody is reading. This must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			sink.send(errors.New("later"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("errSink.send blocked on a full channel")
	}

	if got := <-ch; got != first {
		t.Fatalf("the first error must be the one delivered, got %v", got)
	}
	select {
	case extra := <-ch:
		t.Fatalf("a second error must not be queued, got %v", extra)
	default:
	}
}

// A failure the backend reported itself must not be replaced by "exit status
// 1": the exit code adds nothing, and the parser already sent the reason.
func TestErrSinkPrefersTheBackendReasonOverTheExitStatus(t *testing.T) {
	ch := make(chan error, 1)
	sink := newErrSink(ch)
	sink.send(&DownloadError{Message: "the release has no Tidal URL"})
	sink.send(errors.New("exit status 1"))

	got := <-ch
	var de *DownloadError
	if !errors.As(got, &de) {
		t.Fatalf("want the backend's own reason, got %v", got)
	}
	if de.Message != "the release has no Tidal URL" {
		t.Fatalf("want the backend's message, got %q", de.Message)
	}
}

// A backend that dies WITHOUT emitting its own error event must report its
// exit status. cmd.Wait closes the parent's end of the stdout pipe as soon as
// the process is reaped, so the reader sees "file already closed"; treating
// that as the failure would replace the only real explanation the job has.
func TestPipeTeardownIsNotReportedAsTheFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs /bin/sh")
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 3")
	cmd.SysProcAttr = processGroupAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	events := make(chan ProgressEvent, 8)
	errCh := make(chan error, 4)
	sink := newErrSink(errCh)

	var out bytes.Buffer
	startErr, exitErr, canceled := streamBackend(ctx, cmd, stdout, events, sink.send, &out, nil)
	if startErr != nil {
		t.Fatalf("start: %v", startErr)
	}
	if canceled {
		t.Fatal("nothing cancelled this run")
	}
	if exitErr == nil {
		t.Fatal("want the non-zero exit status")
	}
	sink.send(exitErr)

	select {
	case got := <-errCh:
		if !strings.Contains(got.Error(), "exit status 3") {
			t.Fatalf("want the exit status, got %q", got.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no error reported")
	}
}
