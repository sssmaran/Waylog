package wayloggin

import (
	"bufio"
	"io"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

// Middleware applies the shared Waylog v2 HTTP lifecycle to Gin handlers
// while preserving Gin's route template in fields.http.route.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		wayloghttp.ServeHTTP(c.Writer, c.Request, c.FullPath(), func(w http.ResponseWriter, r *http.Request) {
			c.Request = r
			c.Writer = newResponseWriter(w)
			c.Next()
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
		size:           -1,
	}
}

func (w *responseWriter) WriteHeader(code int) {
	if w.Written() {
		return
	}
	if code > 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
	if w.size < 0 {
		w.size = 0
	}
}

func (w *responseWriter) WriteHeaderNow() {
	if !w.Written() {
		w.WriteHeader(w.status)
	}
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if !w.Written() {
		w.WriteHeader(w.status)
	}
	n, err := w.ResponseWriter.Write(data)
	if w.size < 0 {
		w.size = 0
	}
	w.size += n
	return n, err
}

func (w *responseWriter) WriteString(s string) (int, error) {
	if !w.Written() {
		w.WriteHeader(w.status)
	}
	n, err := io.WriteString(w.ResponseWriter, s)
	if w.size < 0 {
		w.size = 0
	}
	w.size += n
	return n, err
}

func (w *responseWriter) Status() int {
	return w.status
}

func (w *responseWriter) Size() int {
	return w.size
}

func (w *responseWriter) Written() bool {
	return w.size >= 0
}

func (w *responseWriter) Pusher() http.Pusher {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher
	}
	return nil
}

func (w *responseWriter) Flush() {
	w.WriteHeaderNow()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *responseWriter) CloseNotify() <-chan bool {
	closeNotifier, ok := w.ResponseWriter.(http.CloseNotifier)
	if !ok {
		ch := make(chan bool)
		close(ch)
		return ch
	}
	return closeNotifier.CloseNotify()
}
