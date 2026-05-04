package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
	if err := microdemo.InitService("db"); err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/db", wayloghttp.HTTP(microdemo.NewDBHandler()))

	microdemo.RunService("db-demo", ":9084", mux)
}
