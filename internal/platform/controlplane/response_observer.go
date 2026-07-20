package controlplane

import "net/http"

// responseObserver records response metadata without hiding streaming support
// from SSE handlers. Unwrap lets http.ResponseController reach the underlying
// connection for per-stream deadlines.
type responseObserver struct {
	http.ResponseWriter
	status int
	bytes  int
}

func newResponseObserver(writer http.ResponseWriter) *responseObserver {
	return &responseObserver{ResponseWriter: writer}
}

func (w *responseObserver) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseObserver) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(payload)
	w.bytes += written
	return written, err
}

func (w *responseObserver) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseObserver) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseObserver) Committed() bool { return w.status != 0 }

func (w *responseObserver) StatusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *responseObserver) BytesWritten() int { return w.bytes }
