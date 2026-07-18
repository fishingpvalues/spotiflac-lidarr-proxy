package health_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/health"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

func TestCheckHealthyWhenEverythingOK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("#!/bin/bash\necho ok\n"), 0755))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.True(t, result.Healthy)
	assert.Empty(t, result.FailedChecks)
}

func TestCheckUnhealthyWhenCLIMissing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	st := storage.New(dir)

	result := health.Check(db, filepath.Join(dir, "does-not-exist"), st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "cli_executable")
}

func TestCheckUnhealthyWhenCLINotExecutable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("not executable"), 0644))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "cli_executable")
}

func TestCheckUnhealthyWhenDBClosed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.Close()

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "spotiflac-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("#!/bin/bash\necho ok\n"), 0755))
	st := storage.New(dir)

	result := health.Check(db, cliPath, st)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.FailedChecks, "database")
}
