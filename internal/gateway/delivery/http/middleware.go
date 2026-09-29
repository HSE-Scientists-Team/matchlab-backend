package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			http.Error(w, "внутренняя ошибка сервера", http.StatusInternalServerError)
			return
		}
		id := hex.EncodeToString(random[:])
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		wrapped := &responseWriter{ResponseWriter: w}
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(r.Context(), "паника при обработке запроса", "request_id", id, "stack", string(debug.Stack()))
				if wrapped.status == 0 {
					http.Error(wrapped, "внутренняя ошибка сервера", http.StatusInternalServerError)
				}
			}
			if wrapped.status == 0 {
				wrapped.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "HTTP-запрос", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", wrapped.status, "duration", time.Since(started))
		}()
		next.ServeHTTP(wrapped, r)
	})
}
