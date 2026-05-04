package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
	if err := microdemo.InitService("payment"); err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/pay", wayloghttp.HTTP(microdemo.NewPaymentHandler()))

	microdemo.RunService("payment-demo", ":9083", mux)
}
