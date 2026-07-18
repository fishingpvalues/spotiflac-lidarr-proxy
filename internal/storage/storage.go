package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if strings.Contains(nzoID, "..") || strings.Contains(nzoID, "/") || strings.Contains(nzoID, "\\") {
		return "", fmt.Errorf("invalid nzo_id: contains path separators")
	}
	dir := s.JobDir(nzoID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create job dir %s: %w", dir, err)
	}
	return dir, nil
}

func (s *Storage) CleanupJob(nzoID string) error {
	if strings.Contains(nzoID, "..") || strings.Contains(nzoID, "/") || strings.Contains(nzoID, "\\") {
		return fmt.Errorf("invalid nzo_id: contains path separators")
	}
	dir := s.JobDir(nzoID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cleanup job dir %s: %w", dir, err)
	}
	return nil
}

func (s *Storage) GetDiskSpace() (string, string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.outputDir, &stat); err != nil {
		return "", "", fmt.Errorf("statfs %s: %w", s.outputDir, err)
	}
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return formatGB(free), formatGB(total), nil
}

func formatGB(bytes uint64) string {
	gb := float64(bytes) / (1024 * 1024 * 1024)
	return fmt.Sprintf("%.2f", gb)
}
