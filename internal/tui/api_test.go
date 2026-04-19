package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartDashboardStream_ParsesOverviewEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("test server cannot flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Two overview events followed by stream close.
		fmt.Fprint(w, "id: 1\nevent: overview\ndata: {\"window\":\"5m\",\"total_requests\":10,\"total_failures\":1}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "id: 2\nevent: overview\ndata: {\"window\":\"5m\",\"total_requests\":25,\"total_failures\":3}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := NewAPIClient(srv.URL)
	ch, err := client.StartDashboardStream(ctx)
	if err != nil {
		t.Fatalf("StartDashboardStream: %v", err)
	}

	first, ok := (<-ch).(overviewMsg)
	if !ok {
		t.Fatalf("first message was not overviewMsg")
	}
	if first.TotalRequests != 10 || first.TotalFailures != 1 {
		t.Fatalf("first event = %+v, want 10/1", first)
	}

	second, ok := (<-ch).(overviewMsg)
	if !ok {
		t.Fatalf("second message was not overviewMsg")
	}
	if second.TotalRequests != 25 || second.TotalFailures != 3 {
		t.Fatalf("second event = %+v, want 25/3", second)
	}
}

func TestStartDashboardStream_ClosedStreamEmitsErrMsg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Close immediately.
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := NewAPIClient(srv.URL)
	ch, err := client.StartDashboardStream(ctx)
	if err != nil {
		t.Fatalf("StartDashboardStream: %v", err)
	}

	// Drain any pre-close messages then expect a closed channel.
	for range ch {
	}

	// WaitForStream must produce an errMsg when the channel is closed.
	msg := WaitForStream(ch)()
	if _, ok := msg.(errMsg); !ok {
		t.Fatalf("WaitForStream returned %T, want errMsg", msg)
	}
}
