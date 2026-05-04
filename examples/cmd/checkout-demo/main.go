package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	"github.com/sssmaran/WaylogCLI/internal/config"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
	if err := microdemo.InitService("checkout"); err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	paymentURL := config.Getenv("PAYMENT_URL", "http://localhost:9083")
	dbURL := config.Getenv("DB_URL", "http://localhost:9084")

	mux := http.NewServeMux()
	mux.Handle("/checkout", wayloghttp.HTTP(microdemo.NewCheckoutHandler(paymentURL, dbURL)))

	microdemo.RunService("checkout-demo", ":9082", mux)
}
