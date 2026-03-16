package causal

import "math"

// Score computes a confidence value (0.0–1.0) and a tier from the evidence.
func Score(ev Evidence) (float64, ConfidenceTier) {
	liftScore := 0.0
	if ev.Lift > 1 {
		liftScore = math.Log2(ev.Lift) / math.Log2(100)
		if liftScore > 1 {
			liftScore = 1
		}
	}

	proximityScore := 0.0
	if ev.WindowMinutes > 0 {
		proximityScore = 1.0 - (ev.TimeDeltaMin / ev.WindowMinutes)
		if proximityScore < 0 {
			proximityScore = 0
		}
	}

	volumeScore := 0.0
	if ev.AfterFailures > 1 {
		volumeScore = math.Log10(float64(ev.AfterFailures)) / math.Log10(1000)
		if volumeScore > 1 {
			volumeScore = 1
		}
	}

	conf := 0.50*liftScore + 0.25*proximityScore + 0.25*volumeScore

	if conf > 1 {
		conf = 1
	}
	if conf < 0 {
		conf = 0
	}

	tier := TierInsufficient
	if conf >= 0.85 {
		tier = TierSupported
	} else if conf >= 0.70 {
		tier = TierProvisional
	}

	return conf, tier
}
