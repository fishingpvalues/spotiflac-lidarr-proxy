package main

import (
	"testing"

	"github.com/rs/zerolog"

	"github.com/stretchr/testify/assert"
)

func TestResolveLogLevel(t *testing.T) {
	cases := []struct {
		name         string
		configured   string
		verboseCount int
		want         zerolog.Level
	}{
		{"no -v keeps configured level", "info", 0, zerolog.InfoLevel},
		{"single -v raises info to debug", "info", 1, zerolog.DebugLevel},
		{"double -v raises info to trace", "info", 2, zerolog.TraceLevel},
		{"triple -v caps at trace", "info", 3, zerolog.TraceLevel},
		{"-v never lowers an already-verbose config", "trace", 1, zerolog.TraceLevel},
		{"invalid configured level falls back to info, then -v raises it", "bogus", 1, zerolog.DebugLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveLogLevel(tc.configured, tc.verboseCount))
		})
	}
}
