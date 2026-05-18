package microdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

const (
	defaultBurstRequests    = 50
	defaultBurstConcurrency = 10
	incidentSeedPayments    = 6
	maxBurstRequests        = 250
	maxBurstConcurrency     = 50
	maxBurstSamples         = 5
)

type BurstRequest struct {
	Requests    int `json:"requests,omitempty"`
	Concurrency int `json:"concurrency,omitempty"`
}

type BurstSummary struct {
	Requested      BurstRequest   `json:"requested"`
	Accepted       BurstRequest   `json:"accepted"`
	Signals        []SignalResult `json:"signals,omitempty"`
	DurationMs     int64          `json:"duration_ms"`
	ByScenario     map[string]int `json:"by_scenario"`
	OK             int            `json:"ok"`
	Errors         int            `json:"errors"`
	Suppressed     int            `json:"suppressed"`
	SampleTraceIDs []string       `json:"sample_trace_ids"`
}

var scenarioWeights = []struct {
	Cutoff   float64
	Scenario string
}{
	{0.70, ScenarioHappy},
	{0.85, ScenarioPayment502},
	{0.93, ScenarioDBMiss},
	{0.98, ScenarioCheckoutError},
	{1.00, ScenarioSuppressedPayment502},
}

func pickBurstScenarioFloat(x float64) string {
	for _, weight := range scenarioWeights {
		if x < weight.Cutoff {
			return weight.Scenario
		}
	}
	return ScenarioSuppressedPayment502
}

func pickBurstScenario() string {
	return pickBurstScenarioFloat(rand.Float64())
}

func normalizeBurstRequest(raw BurstRequest) (requested, accepted BurstRequest) {
	requested = raw
	if requested.Requests == 0 {
		requested.Requests = defaultBurstRequests
	}
	if requested.Concurrency == 0 {
		requested.Concurrency = defaultBurstConcurrency
	}

	accepted = requested
	if accepted.Requests < 1 {
		accepted.Requests = 1
	}
	if accepted.Requests > maxBurstRequests {
		accepted.Requests = maxBurstRequests
	}
	if accepted.Concurrency < 1 {
		accepted.Concurrency = 1
	}
	if accepted.Concurrency > maxBurstConcurrency {
		accepted.Concurrency = maxBurstConcurrency
	}
	if accepted.Concurrency > accepted.Requests {
		accepted.Concurrency = accepted.Requests
	}
	return requested, accepted
}

func pickBurstScenarioForIndex(i, requests int) string {
	if i < incidentSeedPaymentCount(requests) {
		return ScenarioPayment502
	}
	return pickBurstScenario()
}

func incidentSeedPaymentCount(requests int) int {
	if requests < incidentSeedPayments {
		return requests
	}
	return incidentSeedPayments
}

func runBurst(ctx context.Context, dispatch http.Handler, raw BurstRequest) BurstSummary {
	requested, accepted := normalizeBurstRequest(raw)
	summary := BurstSummary{
		Requested:  requested,
		Accepted:   accepted,
		ByScenario: map[string]int{},
	}
	if dispatch == nil {
		return summary
	}

	start := time.Now()
	sem := make(chan struct{}, accepted.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	sampledScenarios := map[string]struct{}{}

	for i := 0; i < accepted.Requests; i++ {
		if ctx.Err() != nil {
			break
		}
		// Acquire semaphore before spawning so live goroutines stay capped at
		// concurrency instead of stacking up `requests` blocked goroutines.
		sem <- struct{}{}
		wg.Add(1)
		scenario := pickBurstScenarioForIndex(i, accepted.Requests)
		go func(scenario string) {
			defer wg.Done()
			defer func() { <-sem }()

			payload, _ := json.Marshal(PurchaseRequest{
				SKU:      "X1",
				Scenario: scenario,
			})
			req := httptest.NewRequest(http.MethodPost, "/purchase", bytes.NewReader(payload)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			dispatch.ServeHTTP(rec, req)

			var resp struct {
				Success  bool   `json:"success"`
				TraceID  string `json:"trace_id"`
				Scenario string `json:"scenario"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				resp.Scenario = scenario
			}
			if resp.Scenario == "" {
				resp.Scenario = scenario
			}

			mu.Lock()
			defer mu.Unlock()
			summary.ByScenario[resp.Scenario]++
			switch {
			case resp.Scenario == ScenarioSuppressedPayment502:
				summary.Suppressed++
			case resp.Success:
				summary.OK++
			default:
				summary.Errors++
			}
			if resp.TraceID != "" {
				if _, ok := sampledScenarios[resp.Scenario]; !ok && len(sampledScenarios) < maxBurstSamples {
					sampledScenarios[resp.Scenario] = struct{}{}
					summary.SampleTraceIDs = append(summary.SampleTraceIDs, resp.TraceID)
				}
			}
		}(scenario)
	}
	wg.Wait()
	summary.DurationMs = time.Since(start).Milliseconds()
	return summary
}
