package waylogchi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// Middleware applies the shared Waylog v2 HTTP lifecycle to chi handlers while
// preserving the router's route template when available.
func Middleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := ""
		wayloghttp.ServeHTTP(w, r, route, func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rc := chi.RouteContext(r.Context()); rc != nil {
					waylogv2.SetHTTPRoute(r.Context(), rc.RoutePattern())
				}
			}()
			next.ServeHTTP(w, r)
		})
	})
}
