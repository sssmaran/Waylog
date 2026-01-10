package ingest

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/event"
	"github.com/sssmaran/WaylogCLI/internal/graph"
)

var accepted uint64

var graphBuilder = graph.NewBuilder() // GLOBAL GRAPH BUILDER


func SetStore(s *graph.Store) {
	GlobalGraphStore = s
}

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
	// log.Printf("DECISION latency=%d", ev.Metrics.LatencyMs)

	// Next module: tail sampling decision happens here (before Kafka).
// 	if !Sampler.ShouldKeep(ev) {
// 	// Dropped by design — still return 202 so producers never retry
// 		w.WriteHeader(http.StatusAccepted)
// 		return
// }

// 	atomic.AddUint64(&accepted, 1)
// // 	count := atomic.AddUint64(&accepted, 1)
// // 	if count%50 == 0 {
// // 		log.Printf("INGEST: kept events = %d", count) //temp check for every 100 events kept
// // }

// 	// Next module: publish to Kafka.
// 	w.WriteHeader(http.StatusAccepted)
if !Sampler.ShouldKeep(ev) {
	// Dropped by design — still return 202 so producers never retry
	w.WriteHeader(http.StatusAccepted)
	return
}
log.Printf(
  "EVENT status=%d success=%v error=%v",
  ev.Outcome.StatusCode,
  ev.Outcome.Success,
  ev.Error,
)

// BUILD GRAPH FROM REAL EVENT
	g := graphBuilder.Build(ev)
if GlobalGraphStore != nil {
		GlobalGraphStore.Merge(g)
	}

	// GlobalGraphStore.Merge(g)

	// // LOG GRAPH STATS(temp)
	// graphSnapshot := GlobalGraphStore.Graph()
	// log.Printf(
	// 	"GRAPH: merged event, nodes=%d edges=%d status=%d",
	// 	len(graphSnapshot.Nodes),
	// 	len(graphSnapshot.Edges),
	// 	ev.Outcome.StatusCode,
	// )


	atomic.AddUint64(&accepted, 1)
	w.WriteHeader(http.StatusAccepted)
}
