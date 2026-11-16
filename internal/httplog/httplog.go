// Package httplog logs one line per HTTP request served: method, path,
// status, size, duration, remote address and user agent. Request and
// response bodies are never logged.
package httplog

import (
	"log/slog"
	"net/http"
	"time"
)

// Middleware wraps next so that every request it serves is logged to
// logger at Info level once the handler returns. A nil logger uses
// slog.Default().
func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recorder{ResponseWriter: w}
		defer func() {
			status := rw.status
			if status == 0 {
				// Nothing was written: net/http sends 200.
				status = http.StatusOK
			}
			l := logger
			if l == nil {
				l = slog.Default()
			}
			l.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.RequestURI()),
				slog.Int("status", status),
				slog.Int64("bytes", rw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
			)
		}()
		next.ServeHTTP(rw, r)
	})
}

// recorder captures the status code and body size written through it.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	// 1xx responses are informational; the final status comes later.
	if r.status == 0 && code >= 200 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer
// (for Flush, deadlines, and so on).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
