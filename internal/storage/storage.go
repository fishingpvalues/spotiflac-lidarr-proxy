package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Storage struct {
	outputDir string
}

func New(outputDir string) *Storage {
	return &Storage{outputDir: outputDir}
}

func (s *Storage) JobDir(nzoID string) string {
	return filepath.Join(s.outputDir, nzoID)
}

func (s *Storage) PrepareJobDir(nzoID string) (string, error) {
	dir := s.JobDir(nzoID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create job dir %s: %w", dir, err)
	}
	return dir, nil
}

func (s *Storage) CleanupJob(nzoID string) error {
	dir := s.JobDir(nzoID)
	return os.RemoveAll(dir)
}

func (s *Storage) GetDiskSpace() (freeGB string, totalGB string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.outputDir, &stat); err != nil {
		return "0", "0"
	}
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return formatGB(free), formatGB(total)
}

func formatGB(bytes uint64) string {
	gb := float64(bytes) / (1024 * 1024 * 1024)
	return fmt.Sprintf("%.2f", gb)
}
