package doctor

import "testing"

func TestCheckTriageHashIsOK(t *testing.T) {
	c := checkTriageHash()
	if c.Status != StatusOK {
		t.Fatalf("triage hash sanity must be ok, got %q (%s)", c.Status, c.Detail)
	}
	if c.Detail == "" {
		t.Fatal("expected the report hash in the detail")
	}
}
