// Package logger provides a leveled logger backed by Go's stdlib slog
// (Go 1.21+). It exposes Debug/Info/Warn/Error helpers for new code AND
// bridges the stdlib log.Printf API so existing call sites keep working
// at info level without modification — both paths flow through the same
// slog handler and respect the configured level.
//
// Usage:
//
//	f, _ := os.OpenFile("/path/to/quil.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
//	logger.Init("debug", f) // accepts: debug, info, warn, error (case-insensitive)
//	logger.Debug("clipboard: read %d bytes", n)
//	log.Printf("legacy call still works") // routed through slog at info level
package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
)

var (
	mu sync.RWMutex
	sl *slog.Logger
)

// Init configures the global logger to write to w at the given level.
// Acceptable level strings: "debug", "info", "warn"/"warning", "error"/"err"
// (case-insensitive). Unknown levels default to "info".
//
// Init also bridges the stdlib log package so existing log.Printf / log.Println
// call sites are routed through the same slog handler at info level. They are
// dropped if the configured level is warn or error, just like a logger.Info call.
//
// Init is safe to call multiple times — later calls replace the active handler.
//
// All three pieces of global state — the package's `sl`, slog's default
// logger, and the stdlib log writer — are mutated under the same lock so a
// concurrent reader can't observe a half-initialized configuration. In
// practice Init is called once at startup before any goroutines spin up, so
// this is belt-and-braces.
func Init(levelStr string, w io.Writer) {
	level := ParseLevel(levelStr)

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	bridge := slog.NewLogLogger(handler, slog.LevelInfo).Writer()

	mu.Lock()
	defer mu.Unlock()

	sl = logger
	slog.SetDefault(logger)

	// Bridge stdlib log -> slog at info level. Stdlib log calls (e.g. the 152
	// existing log.Printf sites) will go through the slog handler and respect
	// the same level filter. The slog handler adds its own timestamp, so we
	// strip the stdlib log flags to avoid double timestamps.
	log.SetFlags(0)
	log.SetOutput(bridge)
}

// ParseLevel converts a string to a slog.Level. Unknown values yield
// slog.LevelInfo. Exported so tests and config validation can reuse it.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// logAt is the shared body for the level helpers below. It performs an
// Enabled() pre-check so the (potentially expensive) fmt.Sprintf is skipped
// entirely when the configured level filters this call out — important for
// the per-keystroke Debug calls in the TUI hot path.
func logAt(level slog.Level, format string, args ...any) {
	mu.RLock()
	l := sl
	mu.RUnlock()
	if l == nil {
		return
	}
	if !l.Enabled(context.Background(), level) {
		return
	}
	l.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// Debug logs at debug level. Use for verbose diagnostics that should be off
// by default. The variadic args are passed to fmt.Sprintf — keep the format
// string Printf-style to match the existing log.Printf idiom.
func Debug(format string, args ...any) {
	logAt(slog.LevelDebug, format, args...)
}

// Info logs at info level. Replaces log.Printf for new code that wants the
// level to be explicit; existing log.Printf calls still work via the bridge.
func Info(format string, args ...any) {
	logAt(slog.LevelInfo, format, args...)
}

// Warn logs at warn level — degraded behavior, fallback path taken,
// non-fatal misconfiguration, etc.
func Warn(format string, args ...any) {
	logAt(slog.LevelWarn, format, args...)
}

// Error logs at error level — operation failed, user-visible problem.
func Error(format string, args ...any) {
	logAt(slog.LevelError, format, args...)
}
