package main

import (
	"log"
	"net/http"
	"os"

	"github.com/sssmaran/WaylogCLI/internal/ingest"
)

func main() {
	addr := getenv("INGEST_ADDR", ":8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ingest.Health)
	mux.HandleFunc("/v1/events", ingest.Events)

	log.Printf("ingest listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
