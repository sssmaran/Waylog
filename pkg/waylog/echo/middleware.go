package waylogecho

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
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
					waylogv2.Fail(r.Context(), waylogv2.NewError(echoErrorCode(err), waylogv2.WithReason(err.Error())))
					c.Error(err)
				}
			})
			return nil
		}
	}
}

func echoErrorCode(err error) string {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("HTTP_%d", he.Code)
	}
	return "ERR"
}
