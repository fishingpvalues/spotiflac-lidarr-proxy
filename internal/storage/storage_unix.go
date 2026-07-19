//go:build unix

package storage

import (
	"fmt"
	"syscall"
)

func (s *Storage) GetDiskSpace() (float64, float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.outputDir, &stat); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", s.outputDir, err)
	}
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return float64(free) / (1024 * 1024 * 1024), float64(total) / (1024 * 1024 * 1024), nil
}
