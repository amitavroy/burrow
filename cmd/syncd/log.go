package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// parseLevel maps a SYNC_LOG value to a slog level.
func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	switch strings.ToLower(s) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return 0, fmt.Errorf("invalid SYNC_LOG %q (want debug, info, warn or error)", s)
	}
	return l, nil
}

// setupLogger installs the default slog logger. With level set (SYNC_LOG) it
// writes text to stderr at that level; with level empty it writes info and up
// to a rotating file at logPath, so stderr stays free for command output. The
// returned func flushes and closes the file; call it before exiting.
func setupLogger(level string, stderr io.Writer, logPath string) (func() error, error) {
	if level != "" {
		l, err := parseLevel(level)
		if err != nil {
			return nil, err
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: l})))
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &lumberjack.Logger{Filename: logPath, MaxSize: 10, MaxBackups: 10, Compress: true}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return w.Close, nil
}
