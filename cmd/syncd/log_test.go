package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"debug", slog.LevelDebug, true},
		{"INFO", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"bogus", 0, false},
	}
	for _, tt := range tests {
		got, err := parseLevel(tt.in)
		if (err == nil) != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("parseLevel(%q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestSetupLogger(t *testing.T) {
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil))) })

	t.Run("SYNC_LOG writes to stderr at that level", func(t *testing.T) {
		var stderr bytes.Buffer
		closeLog, err := setupLogger("warn", &stderr, filepath.Join(t.TempDir(), "x.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer closeLog()
		slog.Info("hidden")
		slog.Warn("shown", "path", "a.txt")
		if out := stderr.String(); strings.Contains(out, "hidden") || !strings.Contains(out, "shown") || !strings.Contains(out, "path=a.txt") {
			t.Errorf("stderr = %q", out)
		}
	})

	t.Run("bad level is rejected", func(t *testing.T) {
		if _, err := setupLogger("bogus", &bytes.Buffer{}, filepath.Join(t.TempDir(), "x.log")); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("unset writes info to the log file, not stderr", func(t *testing.T) {
		var stderr bytes.Buffer
		path := filepath.Join(t.TempDir(), "logs", "syncd.log")
		closeLog, err := setupLogger("", &stderr, path)
		if err != nil {
			t.Fatal(err)
		}
		slog.Debug("hidden")
		slog.Info("uploaded", "path", "a.txt")
		if err := closeLog(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(data); strings.Contains(got, "hidden") || !strings.Contains(got, "uploaded") {
			t.Errorf("log file = %q", got)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty", stderr.String())
		}
		if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("log dir = %v, %v; want mode 0700", st, err)
		}
	})
}
