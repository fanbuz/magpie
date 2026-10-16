package gateway

import (
	"context"
	"net/http"
	"strings"
)

type transportKey struct{}

// transportWriter observes the response that actually reached the client,
// including an HTTP error in reply to a request that asked to stream.
type transportWriter struct {
	http.ResponseWriter
	transport string
	written   bool
}

func observeTransport(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request) {
	tw := &transportWriter{ResponseWriter: w, transport: "http"}
	return tw, r.WithContext(context.WithValue(r.Context(), transportKey{}, tw))
}
func (w *transportWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *transportWriter) WriteHeader(status int) {
	if !w.written && status >= 200 {
		w.written = true
		if strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
			w.transport = "sse"
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *transportWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *transportWriter) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func httpTransport(h http.Header) string {
	if strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream") {
		return "sse"
	}
	return "http"
}
