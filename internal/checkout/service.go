package checkout

import (
	"math/rand"
	"time"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) Process(req CheckoutRequest) CheckoutResult {
	start := time.Now()

	// ---- feature flags ----
	flags := []string{}
	if rand.Float64() < 0.3 {
		flags = append(flags, "new_checkout")
	}

	// ---- base latency ----
	latency := rand.Int63n(200) + 50 // 50–250ms

	// ---- failure logic ----
	success := true
	status := 200
	var errCode, errMsg string

	// Higher failure rate on new checkout
	if contains(flags, "new_checkout") && rand.Float64() < 0.08 {
		success = false
		status = 502
		errCode = "PMT_502"
		errMsg = "payment gateway failure"
		latency += 300
	}

	// Rare generic failure
	if rand.Float64() < 0.01 {
		success = false
		status = 500
		errCode = "CHK_500"
		errMsg = "checkout internal error"
	}

	// VIP users get more latency sensitivity (realistic)
	if req.User.VIP {
		latency += 150
	}

	time.Sleep(time.Duration(latency) * time.Millisecond)

	return CheckoutResult{
		Success:    success,
		StatusCode: status,
		ErrorCode:  errCode,
		ErrorMsg:   errMsg,
		LatencyMs:  time.Since(start).Milliseconds(),
		Flow:       "checkout_v2",
		Flags:      flags,
	}
}

func contains(arr []string, v string) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}
