package causal

import (
	"testing"
)

func TestScore(t *testing.T) {
	tests := []struct {
		name        string
		evidence    Evidence
		wantTier    ConfidenceTier
		wantMinConf float64
		wantMaxConf float64
	}{
		{
			name: "high lift strong signal → supported",
			evidence: Evidence{
				BeforeFailures: 2,
				AfterFailures:  100,
				Lift:           50.0,
				TimeDeltaMin:   5.0,
				WindowMinutes:  30.0,
			},
			wantTier:    TierProvisional,
			wantMinConf: 0.75,
			wantMaxConf: 0.85,
		},
		{
			name: "moderate lift → provisional",
			evidence: Evidence{
				BeforeFailures: 3,
				AfterFailures:  40,
				Lift:           5.0,
				TimeDeltaMin:   15.0,
				WindowMinutes:  30.0,
			},
			wantTier:    TierInsufficient,
			wantMinConf: 0.40,
			wantMaxConf: 0.70,
		},
		{
			name: "low lift → insufficient",
			evidence: Evidence{
				BeforeFailures: 4,
				AfterFailures:  35,
				Lift:           3.5,
				TimeDeltaMin:   25.0,
				WindowMinutes:  30.0,
			},
			wantTier:    TierInsufficient,
			wantMinConf: 0.0,
			wantMaxConf: 0.70,
		},
		{
			name: "boundary at 0.85 exactly",
			evidence: Evidence{
				BeforeFailures: 1,
				AfterFailures:  80,
				Lift:           20.0,
				TimeDeltaMin:   3.0,
				WindowMinutes:  30.0,
			},
			wantTier:    TierProvisional,
			wantMinConf: 0.70,
			wantMaxConf: 0.80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, tier := Score(tt.evidence)
			if tier != tt.wantTier {
				t.Errorf("tier = %q, want %q (conf=%.4f)", tier, tt.wantTier, conf)
			}
			if conf < tt.wantMinConf || conf > tt.wantMaxConf {
				t.Errorf("confidence = %.4f, want [%.2f, %.2f]", conf, tt.wantMinConf, tt.wantMaxConf)
			}
		})
	}
}

func TestScoreTierBoundaries(t *testing.T) {
	_, tierHigh := Score(Evidence{
		BeforeFailures: 0, AfterFailures: 200, Lift: 100,
		TimeDeltaMin: 1, WindowMinutes: 30,
	})
	if tierHigh != TierSupported {
		t.Errorf("extreme signal should be supported, got %q", tierHigh)
	}

	_, tierZero := Score(Evidence{
		BeforeFailures: 5, AfterFailures: 30, Lift: 3.0,
		TimeDeltaMin: 29, WindowMinutes: 30,
	})
	if tierZero == TierSupported {
		t.Errorf("borderline signal should not be supported, got %q", tierZero)
	}
}
