//go:build !windows

package spotiflac

import (
	"errors"
	"os"
	"syscall"
)

// killGroup kills the whole process group led by pid. Every backend
// subprocess is started with setProcessGroup, so pid is the group leader and
// a negative pid reaches its children - the Chromium and node bridge that
// SpotiFLAC leaves behind and that would otherwise keep the job's stdout
// pipe open.
func killGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// setProcessGroup puts the child in its own process group so killGroup can
// reach its descendants. Setpgid also detaches it from the proxy's terminal
// process group, which is what we want: Ctrl-C on the proxy must not be the
// only thing that stops a backend.
func setProcessGroup(cmd *syscall.SysProcAttr) {
	cmd.Setpgid = true
}

// processGroupAttr returns the SysProcAttr that puts a child in its own
// process group.
func processGroupAttr() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{}
	setProcessGroup(attr)
	return attr
}
