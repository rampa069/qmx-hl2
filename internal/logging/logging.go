package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ANSI colour codes for console output.
const (
	reset  = "\033[0m"
	red    = "\033[1;31m"
	yellow = "\033[1;33m"
	green  = "\033[0;32m"
	cyan   = "\033[0;36m"
	gray   = "\033[0;90m"
)

// colourHandler wraps slog.Handler to add colours on terminals.
type colourHandler struct {
	out   io.Writer
	level slog.Leveler
	mu    sync.Mutex
}

func (h *colourHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *colourHandler) Handle(_ context.Context, r slog.Record) error {
	var col string
	switch {
	case r.Level >= slog.LevelError:
		col = red
	case r.Level >= slog.LevelWarn:
		col = yellow
	case r.Level >= slog.LevelInfo:
		col = green
	default:
		col = gray
	}

	ts := r.Time.Format("15:04:05.000")
	tag := r.Level.String()

	// Append key=value attrs so they are visible on the console
	attrs := ""
	r.Attrs(func(a slog.Attr) bool {
		attrs += " " + a.Key + "=" + a.Value.String()
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintf(h.out, "%s%s %-5s%s %s%s\n", col, ts, tag, reset, r.Message, attrs)
	return err
}

func (h *colourHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *colourHandler) WithGroup(name string) slog.Handler       { return h }

// Setup initialises the global slog logger.
//   - Always writes to a timestamped log file in logsDir.
//   - If verbose, also writes coloured output to stderr.
//
// Returns the log file path so callers can report it.
func Setup(logsDir, overridePath string, verbose bool) (string, error) {
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}

	logPath := overridePath
	if logPath == "" {
		ts := time.Now().Format("20060102_150405")
		logPath = filepath.Join(logsDir, fmt.Sprintf("qmx-hl2-%s.log", ts))
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("open log file: %w", err)
	}

	// Write header
	fmt.Fprintf(f, "qmx-hl2 log started at %s\n", time.Now().UTC())
	fmt.Fprintf(f, "Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(f, "================================================================================")

	// File handler: everything at DEBUG and above, plain text.
	fileHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})

	var handler slog.Handler
	if verbose {
		// Fan-out to both file and coloured console
		handler = &fanoutHandler{
			handlers: []slog.Handler{
				fileHandler,
				&colourHandler{out: os.Stderr, level: slog.LevelDebug},
			},
		}
	} else {
		// Console only shows warnings+; file gets everything
		handler = &fanoutHandler{
			handlers: []slog.Handler{
				fileHandler,
				&colourHandler{out: os.Stderr, level: slog.LevelWarn},
			},
		}
	}

	slog.SetDefault(slog.New(handler))
	return logPath, nil
}

// fanoutHandler dispatches to multiple handlers.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (f *fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: hs}
}

func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: hs}
}
