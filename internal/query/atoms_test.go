package query

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/store"
)

func TestEqualsPredicateEval_FlattenedFacts(t *testing.T) {
	f := store.RequestFacts{
		Services:     []string{"checkout"},
		Errors:       []string{"PMT_502"},
		UserID:       "user-123",
		UserTier:     "premium",
		UserVIP:      true,
		UserRegion:   "us-west-2",
		FeatureFlags: []string{"flag-a", "flag-b"},
		Status:       "failed",
		Version:      "v2",
	}

	cases := []struct {
		name  string
		field string
		value string
		want  bool
	}{
		{"service", "service", "checkout", true},
		{"error", "error_code", "PMT_502", true},
		{"feature_flag", "feature_flag", "flag-a", true},
		{"flag alias", "flag", "flag-b", true},
		{"tier alias", "tier", "premium", true},
		{"user_tier", "user_tier", "premium", true},
		{"user_id", "user_id", "user-123", true},
		{"user_region", "user_region", "us-west-2", true},
		{"user_vip true", "user_vip", "true", true},
		{"user_vip false", "user_vip", "false", false},
		{"version", "version", "v2", true},
		{"status", "status", "failed", true},
		{"missing", "feature_flag", "missing", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EqualsPredicate{Field: tc.field, Value: tc.value}.Eval(f)
			if got != tc.want {
				t.Fatalf("Eval(%s=%s) = %v, want %v", tc.field, tc.value, got, tc.want)
			}
		})
	}
}

func TestEqualsPredicateEval_TierAlias(t *testing.T) {
	f := store.RequestFacts{
		UserTier: "standard",
	}
	if !(EqualsPredicate{Field: "tier", Value: "standard"}.Eval(f)) {
		t.Fatal("tier should match UserTier")
	}
	if !(EqualsPredicate{Field: "user_tier", Value: "standard"}.Eval(f)) {
		t.Fatal("user_tier should match UserTier")
	}
}
