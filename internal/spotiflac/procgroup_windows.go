//go:build windows

package spotiflac

import (
	"os"
	"syscall"
)

// killGroup has no process-group equivalent on Windows; the caller falls
// back to killing the direct child.
func killGroup(int) error { return os.ErrProcessDone }

func setProcessGroup(*syscall.SysProcAttr) {}

// processGroupAttr returns the (empty) SysProcAttr for this platform.
func processGroupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
