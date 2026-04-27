# SDK Examples

These are the recommended copy-paste integration shapes for Waylog v2. Prefer framework middleware plus a request-scoped logger. Use low-level `Begin` / `Finalize` APIs only when writing a custom adapter or a deterministic test/smoke driver.

## Go `net/http`

```go
package main

import (
	"context"
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
	_ = waylog.Init(waylog.Config{
		Service:   "checkout",
		Env:       "prod",
		IngestURL: "http://localhost:8080",
	})
	defer waylog.Shutdown(context.Background())

	mux := http.NewServeMux()
	mux.Handle("/buy", wayloghttp.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		waylog.From(r.Context()).Info("cart loaded", waylog.F{"cart_id": "c_123"})
		w.WriteHeader(http.StatusOK)
	})))

	_ = http.ListenAndServe(":3000", mux)
}
```

## Go chi

```go
import (
	"net/http"

	"github.com/go-chi/chi/v5"
	waylogchi "github.com/sssmaran/WaylogCLI/pkg/waylog/chi"
	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

r := chi.NewRouter()
r.Use(waylogchi.Middleware)
r.Post("/buy/{id}", func(w http.ResponseWriter, r *http.Request) {
	waylog.From(r.Context()).Info("checkout started")
	w.WriteHeader(http.StatusOK)
})
```

## Go gin

```go
import (
	"net/http"

	"github.com/gin-gonic/gin"
	wayloggin "github.com/sssmaran/WaylogCLI/pkg/waylog/gin"
	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

r := gin.New()
r.Use(wayloggin.Middleware())
r.POST("/buy/:id", func(c *gin.Context) {
	waylog.From(c.Request.Context()).Info("checkout started")
	c.Status(http.StatusOK)
})
```

## Go echo

```go
import (
	"net/http"

	"github.com/labstack/echo/v4"
	waylogecho "github.com/sssmaran/WaylogCLI/pkg/waylog/echo"
	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

e := echo.New()
e.Use(waylogecho.Middleware())
e.POST("/buy/:id", func(c echo.Context) error {
	waylog.From(c.Request().Context()).Info("checkout started")
	return c.NoContent(http.StatusOK)
})
```

## TypeScript Standalone

```ts
import { begin, finalize, from, init, setField, step } from "@waylog/sdk";

init({
  service: "worker",
  env: "prod",
  ingestUrl: "http://localhost:8080",
  apiKey: process.env.WAYLOG_WRITE_KEY,
});

const ctx = begin({});
setField(ctx, "http", { method: "JOB", route: "queue:purchase", status: 200 });
await step(ctx, "payment.charge", async () => {
  from(ctx).info("charging payment", { cart_id: "c_123" });
});
await finalize(ctx);
```

## TypeScript Express

```ts
import { waylog, useLogger } from "@waylog/sdk/express";

app.use(waylog({
  service: "checkout",
  env: "prod",
  ingestUrl: "http://localhost:8080",
  apiKey: process.env.WAYLOG_WRITE_KEY,
}));

app.post("/buy/:id", (req, res) => {
  useLogger(req).info("checkout started", { cart_id: req.params.id });
  res.sendStatus(200);
});
```

## TypeScript Hono

```ts
import { Hono } from "hono";
import { waylog, useLogger } from "@waylog/sdk/hono";

const app = new Hono();
app.use("*", waylog({
  service: "checkout",
  env: "prod",
  ingestUrl: "http://localhost:8080",
}));

app.post("/buy/:id", (c) => {
  useLogger(c).info("checkout started", { cart_id: c.req.param("id") });
  return c.text("ok");
});
```

## TypeScript Next.js

```ts
import { middleware as withWaylog, useLogger } from "@waylog/sdk/next";

export const POST = withWaylog({
  service: "checkout",
  env: "prod",
  ingestUrl: "http://localhost:8080",
}, async (_req, ctx) => {
  useLogger(ctx).info("checkout started");
  return Response.json({ ok: true }, { status: 200 });
});
```

## TypeScript NestJS

```ts
import { MiddlewareConsumer, Module, NestModule } from "@nestjs/common";
import { middleware as waylog } from "@waylog/sdk/nest";

@Module({})
export class AppModule implements NestModule {
  configure(consumer: MiddlewareConsumer) {
    consumer
      .apply(waylog({
        service: "checkout",
        env: "prod",
        ingestUrl: "http://localhost:8080",
      }))
      .forRoutes("*");
  }
}
```
