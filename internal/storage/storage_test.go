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
	free, total, err := s.GetDiskSpace()
	assert.NoError(t, err)
	assert.NotEmpty(t, free)
	assert.NotEmpty(t, total)
}

func TestCountAudioFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "01.flac"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "02.flac"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("x"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "Disc 2"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Disc 2", "03.flac"), []byte("x"), 0644))

	count, err := storage.CountAudioFiles(dir)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}
