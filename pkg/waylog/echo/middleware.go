package waylogecho

import (
	"net/http"

	"github.com/labstack/echo/v4"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

// Middleware applies the shared Waylog v2 HTTP lifecycle to Echo handlers
// while preserving Echo's route template in fields.http.route.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wayloghttp.ServeHTTP(c.Response().Writer, c.Request(), c.Path(), func(w http.ResponseWriter, r *http.Request) {
				c.SetRequest(r)
				c.Response().Writer = w
				if err := next(c); err != nil {
					c.Error(err)
				}
			})
			return nil
		}
	}
}
