package spotiflac

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// This file owns the one thing that must be true of every backend
// subprocess: waiting for it always ends, and it always ends within a
// bounded time of the job's context ending.
//
// The obvious shape - read stdout to EOF, then call Wait - does not have
// that property. exec.CommandContext kills only the direct child, and the
// SpotiFLAC CLI does not clean up after itself: a finished job leaves its
// Chromium and the node extension bridge behind (see reapStaleBrowsers).
// Those grandchildren inherit the write end of the CLI's stdout pipe, so
// after the CLI exits the pipe still never reaches EOF. The reader then
// never returns, Wait is never reached, attemptDownload never sees a
// terminal event, and the job sits in "Downloading" holding its concurrency
// slot for good - which is precisely what an external monitor sees as
// "slots in the queue, 0 B/s". Measured on potatostack: that monitor
// restarted the container eight times in the week to 2026-10-02, each time
// throwing away the breakers, the circuit park and every in-flight job.
//
// Three ways out, all bounded: the process exits (then at most
// outputDrainGrace of trailing output is still collected), the context ends
// (then the process group is killed), or the drain deadline passes (then the
// pipe is abandoned and what was read is kept).

// outputDrainGrace is how long a finished process's pipe is still read
// before it is abandoned. It only has to cover output already flushed into
// the pipe buffer, not more work by the backend.
const outputDrainGrace = 2 * time.Second

// streamBackend starts cmd, reads its stdout as progress events while
// watching ctx, and returns only when the process is gone or ctx has ended.
//
// stdout must be a pipe obtained from cmd.StdoutPipe(). events is the
// caller's channel and is NOT closed here. onErr receives at most one
// terminal error. A failure to START the process is returned as startErr
// with canceled=false and no reader goroutine left behind.
func streamBackend(
	ctx context.Context,
	cmd *exec.Cmd,
	stdout io.ReadCloser,
	events chan<- ProgressEvent,
	onErr func(error),
	outputBuf *bytes.Buffer,
	onVerify func(ProgressEvent),
) (startErr error, exitErr error, canceled bool) {
	if err := cmd.Start(); err != nil {
		return err, nil, false
	}

	// The reader runs in its own goroutine so that a pipe held open by a
	// surviving grandchild cannot pin the job. Everything read is teed into
	// outputBuf: the CLI's own reason lines ("NETWORK_ERROR: Timeout (120s)
	// calling download") appear only there, and they are the explanation a
	// failure has.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		parseProgress(io.TeeReader(stdout, outputBuf), events, outputBuf, onVerify, func(err error) {
			if isPipeTeardown(err) {
				return
			}
			onErr(err)
		})
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		// The process is gone. Read briefly for the tail of its output,
		// then stop waiting for an EOF that may never come.
		select {
		case <-readDone:
		case <-time.After(outputDrainGrace):
			abandonPipe(stdout)
			<-readDone
		}
		return nil, err, false
	case <-ctx.Done():
		killProcessGroup(cmd)
		select {
		case <-waitDone:
		case <-time.After(outputDrainGrace):
		}
		abandonPipe(stdout)
		<-readDone
		return nil, ctx.Err(), true
	}
}

// isPipeTeardown reports whether a read error is just the pipe being torn
// down under the reader rather than the backend failing.
//
// Two things tear it down: cmd.Wait closes the parent's end of a StdoutPipe
// as soon as the process is reaped ("it is thus incorrect to call Wait before
// all reads from the pipe have completed" - which is exactly why Wait now
// runs alongside the reader instead of after it), and abandonPipe sets a read
// deadline. Reporting either would replace the job's real failure - typically
// the exit status - with "file already closed".
func isPipeTeardown(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
}

// abandonPipe releases a reader blocked on a pipe that a surviving grandchild
// is holding open. SetReadDeadline is supported for pipes and makes the
// blocked Read return; closing would work too but loses buffered output.
func abandonPipe(stdout io.ReadCloser) {
	if d, ok := stdout.(interface{ SetReadDeadline(time.Time) error }); ok {
		if err := d.SetReadDeadline(time.Now()); err == nil {
			return
		}
	}
	_ = stdout.Close()
}

// killProcessGroup kills cmd and everything it spawned. SpotiFLAC's browsers
// and extension bridge outlive the CLI on purpose, so killing only the direct
// child leaves them holding the output pipe.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := killGroup(cmd.Process.Pid); err != nil && !errors.Is(err, exec.ErrNotFound) {
		_ = cmd.Process.Kill()
	}
}

// errSink delivers the first terminal error of a backend run and never
// blocks. It replaces the old capacity-1 channel written from two places
// (the parser on an "error" event, then the exit status after Wait): the
// handler returns on the first error it receives, so the second send had no
// receiver and blocked its goroutine - and its two channels - forever, once
// per failed attempt.
type errSink struct {
	ch   chan<- error
	sent bool
}

func newErrSink(ch chan<- error) *errSink { return &errSink{ch: ch} }

func (s *errSink) send(err error) {
	if err == nil || s.sent {
		return
	}
	s.sent = true
	select {
	case s.ch <- err:
	default:
		// The consumer may already have returned on a "complete" event.
		// Dropping a diagnostic beats leaking a goroutine.
	}
}
