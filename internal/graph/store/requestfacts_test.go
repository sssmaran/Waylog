package store

import "testing"

func TestRequestFacts_HasFeatureFlag(t *testing.T) {
	f := RequestFacts{
		UserTier:     "premium",
		FeatureFlags: []string{"flag-a", "flag-b"},
	}

	if !f.HasFeatureFlag("flag-a") {
		t.Fatal("HasFeatureFlag should match flag-a")
	}
	if !f.HasFeatureFlag("flag-b") {
		t.Fatal("HasFeatureFlag should match flag-b")
	}
	if f.HasFeatureFlag("missing") {
		t.Fatal("HasFeatureFlag should return false for unknown flags")
	}
}

func TestRequestFacts_HasError(t *testing.T) {
	f := RequestFacts{
		Errors: []string{"ERR_500", "ERR_404"},
	}
	if !f.HasError("ERR_500") {
		t.Fatal("HasError should match ERR_500")
	}
	if f.HasError("ERR_999") {
		t.Fatal("HasError should return false for unknown error")
	}
}
