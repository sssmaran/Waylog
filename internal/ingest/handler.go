package ingest

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/event"
)

var accepted uint64

func Health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func Events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var ev event.WideEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&ev); err != nil {
		log.Println("INGEST: json decode failed:", err) //if it fails logginf the err
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	log.Println("INGEST: event received")


	// Ensure server-side timestamp sanity (optional, but helpful)
	if ev.Timestamp.After(time.Now().Add(5 * time.Minute)) {
		http.Error(w, "timestamp too far in future", http.StatusBadRequest)
		return
	}

	if err := ev.Validate(); err != nil {
		log.Println("INGEST: event validation failed:", err) //logging validation failed error
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Println("INGEST: event accepted") //logging event accepted


	atomic.AddUint64(&accepted, 1)

	// Next module: tail sampling decision happens here (before Kafka).
	// Next module: publish to Kafka.
	w.WriteHeader(http.StatusAccepted)
}
