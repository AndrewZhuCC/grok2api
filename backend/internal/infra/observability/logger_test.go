package observability

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"  debug  ", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		// An unset or misspelled value must not silence logs.
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
	}
	for _, test := range cases {
		if got := parseLogLevel(test.in); got != test.want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", test.in, got, test.want)
		}
	}
}

func TestNewLoggerHonorsDebugEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	if !NewLogger().Enabled(nil, slog.LevelDebug) {
		t.Fatal("LOG_LEVEL=debug must enable debug records")
	}

	t.Setenv("LOG_LEVEL", "")
	logger := NewLogger()
	if logger.Enabled(nil, slog.LevelDebug) {
		t.Fatal("default level must not emit debug records")
	}
	if !logger.Enabled(nil, slog.LevelInfo) {
		t.Fatal("default level must still emit info records")
	}
}
