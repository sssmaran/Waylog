package ingestv2

import (
	"fmt"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestDedupEvictsOldestAtCapacity(t *testing.T) {
	d := NewDedup(1024, nil)
	for i := 0; i < 2048; i++ {
		d.Add(fmt.Sprintf("event-%04d", i))
	}
	if got := d.Size(); got != 1024 {
		t.Fatalf("Size=%d want 1024", got)
	}
	for i := 0; i < 1024; i++ {
		if d.Seen(fmt.Sprintf("event-%04d", i)) {
			t.Fatalf("old event %d should be evicted", i)
		}
	}
	for i := 1024; i < 2048; i++ {
		if !d.Seen(fmt.Sprintf("event-%04d", i)) {
			t.Fatalf("new event %d should be present", i)
		}
	}
}

func TestDedupSeenDoesNotPromote(t *testing.T) {
	d := NewDedup(3, nil)
	d.Add("a")
	d.Add("b")
	d.Add("c")
	if !d.Seen("a") {
		t.Fatal("a should be present before eviction")
	}
	d.Add("d")
	if d.Seen("a") {
		t.Fatal("Seen should not promote a; it should be evicted")
	}
}

func TestDedupConcurrent(t *testing.T) {
	d := NewDedup(256, nil)
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				id := fmt.Sprintf("g-%02d-%04d", g, i)
				d.Add(id)
				_ = d.Seen(id)
			}
		}(g)
	}
	wg.Wait()
	if got := d.Size(); got > 256 {
		t.Fatalf("Size=%d want <=256", got)
	}
}

func TestDedupGaugeTracksSize(t *testing.T) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_event_dedup_size", Help: "test"})
	d := NewDedup(2, g)
	d.Add("a")
	d.Add("b")
	d.Add("c")

	var metric dto.Metric
	if err := g.Write(&metric); err != nil {
		t.Fatal(err)
	}
	if got := metric.GetGauge().GetValue(); got != 2 {
		t.Fatalf("gauge=%v want 2", got)
	}
}

func TestDedupRemove(t *testing.T) {
	d := NewDedup(10, nil)
	d.Add("a")
	if !d.Seen("a") {
		t.Fatal("a should be present")
	}
	d.Remove("a")
	if d.Seen("a") {
		t.Fatal("a should be removed")
	}
	if got := d.Size(); got != 0 {
		t.Fatalf("Size=%d want 0", got)
	}
}
