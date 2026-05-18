package incidents

import (
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func TestStableIDUsesFiveMinuteBucket(t *testing.T) {
	family := apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"}
	base := time.Date(2026, 5, 4, 12, 3, 0, 0, time.UTC)
	a := StableID("prod", family, base)
	b := StableID("prod", family, base.Add(90*time.Second))
	c := StableID("prod", family, base.Add(3*time.Minute))
	if a != b {
		t.Fatalf("same bucket ids differ: %s %s", a, b)
	}
	if a == c {
		t.Fatalf("different bucket id did not change: %s", a)
	}
	if len(a) != len("inc_")+16 {
		t.Fatalf("id length=%d id=%s", len(a), a)
	}
}
