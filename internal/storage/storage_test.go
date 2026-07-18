package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

func TestPrepareAndCleanupJobDir(t *testing.T) {
	dir := t.TempDir()
	s := storage.New(dir)

	jobDir, err := s.PrepareJobDir("test-nzo-001")
	require.NoError(t, err)
	assert.DirExists(t, jobDir)
	assert.Equal(t, filepath.Join(dir, "test-nzo-001"), jobDir)

	testFile := filepath.Join(jobDir, "test.flac")
	require.NoError(t, os.WriteFile(testFile, []byte("fake-flac"), 0644))

	err = s.CleanupJob("test-nzo-001")
	require.NoError(t, err)
	assert.NoDirExists(t, jobDir)
}

func TestGetDiskSpace(t *testing.T) {
	s := storage.New(t.TempDir())
	free, total := s.GetDiskSpace()
	assert.NotEmpty(t, free)
	assert.NotEmpty(t, total)
}
