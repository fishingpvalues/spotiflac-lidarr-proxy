//go:build windows

package storage

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func (s *Storage) GetDiskSpace() (float64, float64, error) {
	var freeBytes, totalBytes, totalFreeBytes uint64
	path, err := windows.UTF16PtrFromString(s.outputDir)
	if err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", s.outputDir, err)
	}
	if err := windows.GetDiskFreeSpaceEx(path, &freeBytes, &totalBytes, &totalFreeBytes); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", s.outputDir, err)
	}
	return float64(freeBytes) / (1024 * 1024 * 1024), float64(totalBytes) / (1024 * 1024 * 1024), nil
}
