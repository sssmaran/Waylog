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
	if err := microdemo.InitService("api-gateway"); err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	checkoutURL := config.Getenv("CHECKOUT_URL", "http://localhost:9082")
	gateway := microdemo.NewGatewayHandler(checkoutURL)

	mux := http.NewServeMux()
	mux.Handle("/purchase", wayloghttp.HTTP(http.HandlerFunc(gateway.ServePurchase)))
	mux.HandleFunc("/demo", gateway.ServeDemo)

	microdemo.RunService("api-gateway", ":9081", mux)
}
