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

type scenarioWeight struct {
	Cutoff   float64
	Scenario string
}

// scenarioWeights is the base cumulative-cutoff table for NON-seeded burst
// traffic. Each burst jitters a copy of it (see jitteredScenarioWeights) so
// repeated `make demo` runs look different; the deterministic incident seeds in
// pickBurstScenarioForIndex never consult this table.
var scenarioWeights = []scenarioWeight{
	{0.68, ScenarioHappy},
	{0.83, ScenarioPayment502},
	{0.90, ScenarioDBMiss},
	{0.95, ScenarioCheckoutError},
	{0.98, ScenarioInventory503},
	{0.995, ScenarioCheckoutPanic},
	{1.00, ScenarioSuppressedPayment502},
}

// burstWeightJitterPct bounds how far each scenario band's width may drift from
// the base table per burst. Kept small so every scenario stays well-represented.
const burstWeightJitterPct = 0.05

func pickBurstScenarioFloatFrom(x float64, weights []scenarioWeight) string {
	for _, weight := range weights {
		if x < weight.Cutoff {
			return weight.Scenario
		}
	}
	return ScenarioSuppressedPayment502
}

func pickBurstScenarioFloat(x float64) string {
	return pickBurstScenarioFloatFrom(x, scenarioWeights)
}

// jitteredScenarioWeights returns a per-burst copy of scenarioWeights with each
// band's width perturbed by up to ±burstWeightJitterPct, then renormalized so
// cutoffs stay strictly increasing and end exactly at 1.0. Scenario order is
// preserved, so every scenario remains reachable — only the proportions of
// non-seeded traffic shift between bursts.
func jitteredScenarioWeights() []scenarioWeight {
	out := make([]scenarioWeight, len(scenarioWeights))
	widths := make([]float64, len(scenarioWeights))
	prev, total := 0.0, 0.0
	for i, w := range scenarioWeights {
		jitter := 1 + (rand.Float64()*2-1)*burstWeightJitterPct
		widths[i] = (w.Cutoff - prev) * jitter
		prev = w.Cutoff
		total += widths[i]
	}
	cum := 0.0
	for i := range scenarioWeights {
		cum += widths[i] / total
		out[i] = scenarioWeight{Cutoff: cum, Scenario: scenarioWeights[i].Scenario}
	}
	out[len(out)-1].Cutoff = 1.0 // pin the final cutoff against float drift
	return out
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

func pickBurstScenarioForIndex(i, requests int, weights []scenarioWeight) string {
	seeds := incidentSeedPaymentCount(requests)
	if i < seeds {
		return ScenarioPayment502
	}
	// Deterministically seed exactly one checkout panic right after the payment
	// seeds (within the PMT_502 timing window) so the acceptance gate always has
	// app-runtime evidence; a weighted-only panic can be missed at low request
	// counts. The seed branches above never consult weights, so per-burst jitter
	// can never weaken the deterministic incident the acceptance gate depends on.
	if i == seeds && requests > seeds {
		return ScenarioCheckoutPanic
	}
	return pickBurstScenarioFloatFrom(rand.Float64(), weights)
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
	// One jittered weight table per burst so each run's non-seeded traffic mix
	// differs while the deterministic seeds stay fixed.
	weights := jitteredScenarioWeights()

	for i := 0; i < accepted.Requests; i++ {
		if ctx.Err() != nil {
			break
		}
		// Acquire semaphore before spawning so live goroutines stay capped at
		// concurrency instead of stacking up `requests` blocked goroutines.
		sem <- struct{}{}
		wg.Add(1)
		scenario := pickBurstScenarioForIndex(i, accepted.Requests, weights)
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
