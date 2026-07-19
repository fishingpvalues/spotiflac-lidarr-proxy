package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

var audioExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".ogg":  true,
	".opus": true,
}

// CountAudioFiles walks dir recursively (to cover multi-disc subfolders)
// and returns the number of files with a recognized audio extension.
func CountAudioFiles(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if audioExtensions[strings.ToLower(filepath.Ext(path))] {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("count audio files in %s: %w", dir, err)
	}
	return count, nil
}
