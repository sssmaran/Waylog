package microdemo

import (
	"net/http"
	"time"
)

func demoHTTPTransport() *http.Transport {
	return &http.Transport{
		MaxConnsPerHost:     64,
		MaxIdleConnsPerHost: 32,
		MaxIdleConns:        128,
		IdleConnTimeout:     30 * time.Second,
	}
}
