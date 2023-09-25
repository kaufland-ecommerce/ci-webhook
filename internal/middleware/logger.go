package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type LogFormatter func(r *http.Request) middleware.LogEntry

func (l LogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return l(r)
}

func NewLogFormatter(logger *slog.Logger) middleware.LogFormatter {
	return LogFormatter(func(r *http.Request) middleware.LogEntry {
		return &LogEntry{
			req:    r,
			logger: logger,
		}
	})
}

// LogEntry represents an individual log entry.
type LogEntry struct {
	req    *http.Request
	logger *slog.Logger
}

// Write constructs and writes the final log entry.
func (l *LogEntry) Write(status, totalBytes int, _ http.Header, elapsed time.Duration, _ any) {
	rid := GetReqID(l.req.Context())
	l.logger.LogAttrs(nil, slog.LevelInfo, "handled",
		slog.String("http.request_id", rid),
		slog.String("http.method", l.req.Method),
		slog.String("http.url", l.req.RequestURI),
		slog.String("http.url_details.host", l.req.Host),
		slog.Int("http.status", status),
		slog.Duration("http.duration", elapsed),
		slog.Int("network.bytes_written", totalBytes),
	)
}

// Panic prints the call stack for a panic.
func (l *LogEntry) Panic(v interface{}, stack []byte) {
	l.logger.LogAttrs(nil, slog.LevelError, "request caused panic",
		slog.String("error.kind", "panic"),
		slog.String("error.message", fmt.Sprintf("%#v", v)),
		slog.String("error.stack", string(stack)),
	)
}
