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
// bounded time of the job's context ending - without losing any of its
// output.
//
// exec.CommandContext kills only the direct child, and the SpotiFLAC CLI does
// not clean up after itself: a finished job leaves its Chromium and the node
// extension bridge running (see reapStaleBrowsers). Those grandchildren
// inherit the CLI's stdout, so a pipe's EOF does not arrive when the CLI
// exits. Reading such a pipe to EOF and only then calling Wait - the obvious
// shape, and the one this used to have - can therefore block forever:
// attemptDownload never sees a terminal event, the job sits in "Downloading"
// holding its concurrency slot, and the 2xJobTimeout budget cannot stop it
// because nothing it can cancel is still alive. Measured on potatostack, that
// is exactly the state the external monitor reads as "slots in the queue,
// 0 B/s"; it restarted this container eight times in the week to 2026-10-02.
//
// The pipes are therefore OURS: created with os.Pipe and handed to the child
// as *os.File, which os/exec passes straight through without managing them
// (see its writerDescriptor - a user-supplied *os.File is neither copied by a
// goroutine nor closed by Wait). That is what makes a bounded drain possible
// at all. With cmd.StdoutPipe, Wait closes the read end the moment the process
// is reaped, so buffered output - up to 64 KB of it - can be thrown away, and
// the 100-event case in client_test.go lost half its events under -race.
//
// Three ways out, all bounded: the writers close (normal), the context ends
// (then the process group is killed), or outputDrainGrace passes after the
// process exited (then the pipe is abandoned and what was read is kept).

// outputDrainGrace is how long a pipe is still read after the process that
// owns it has exited. It only has to cover output already flushed into the
// pipe buffer, not more work by the backend.
const outputDrainGrace = 2 * time.Second

// streamBackend starts cmd and reads its stdout as progress events while
// watching ctx. It returns only when the process is gone, or when ctx has
// ended, and only once every byte the backend wrote has been read.
//
// events is the caller's channel and is NOT closed here. onErr receives at
// most one terminal error. stderr is captured into stderrBuf with the same
// bounded read.
func streamBackend(
	ctx context.Context,
	cmd *exec.Cmd,
	events chan<- ProgressEvent,
	onErr func(error),
	outputBuf *bytes.Buffer,
	stderrBuf *bytes.Buffer,
	onVerify func(ProgressEvent),
) (startErr error, exitErr error, canceled bool) {
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return err, nil, false
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return err, nil, false
	}

	// Passing *os.File values means os/exec hands the descriptors to the
	// child directly: no copier goroutines, and Wait does not close them.
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	closeAll := func() {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
	}

	if err := cmd.Start(); err != nil {
		closeAll()
		return err, nil, false
	}
	// The parent must not keep the write ends open, or EOF would never
	// arrive even after every descendant has exited.
	stdoutW.Close()
	stderrW.Close()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		parseProgress(io.TeeReader(stdoutR, outputBuf), events, outputBuf, onVerify, func(err error) {
			if isPipeTeardown(err) {
				return
			}
			onErr(err)
		})
	}()

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderrR)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	drain := func() {
		drainPipe(stdoutR, readDone)
		drainPipe(stderrR, stderrDone)
	}

	select {
	case err := <-waitDone:
		drain()
		return nil, err, false
	case <-ctx.Done():
		killProcessGroup(cmd)
		select {
		case <-waitDone:
		case <-time.After(outputDrainGrace):
		}
		drain()
		return nil, ctx.Err(), true
	}
}

// drainPipe waits for a reader to reach the end of its pipe, and abandons it
// after outputDrainGrace. A deadline, not a Close: the bytes already in the
// buffer are still delivered, and the reader returns instead of leaking.
func drainPipe(pipe *os.File, done <-chan struct{}) {
	select {
	case <-done:
		return
	case <-time.After(outputDrainGrace):
		_ = pipe.SetReadDeadline(time.Now())
	}
	select {
	case <-done:
	case <-time.After(outputDrainGrace):
	}
}

// isPipeTeardown reports whether a read error is just the pipe being torn
// down under the reader rather than the backend failing.
//
// abandonPipe/drainPipe set a read deadline once the process is gone, and the
// resulting i/o timeout is not a backend failure. Reporting it would replace
// the job's real reason - typically the exit status - with "i/o timeout".
func isPipeTeardown(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
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
