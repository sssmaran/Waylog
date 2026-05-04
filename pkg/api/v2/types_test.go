package apiv2

import "testing"

func TestErrorFamilyRoundTrip(t *testing.T) {
	cases := []ErrorFamily{
		{Service: "svc", Step: "step", ErrorCode: "CODE"},
		{Service: "svc:a", Step: "step:b", ErrorCode: "CODE"},
		{Service: "svc", Step: "step", ErrorCode: "CODE:WITH:COLON"},
	}
	for _, tc := range cases {
		display := FormatErrorFamily(tc)
		key, ok := ParseErrorFamily(display)
		if !ok {
			t.Fatalf("ParseErrorFamily(%q) failed", display)
		}
		if key.Service != tc.Service || key.Step != tc.Step || key.ErrorCode != tc.ErrorCode {
			t.Fatalf("round trip=%+v want %+v display=%q", key, tc, display)
		}
	}
}

func TestParseErrorFamilyRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"a:b", `a:b:c\`, `a:b:c:d`, `a:\x:c`, ":b:c", "a::c", "a:b:"} {
		if _, ok := ParseErrorFamily(raw); ok {
			t.Fatalf("expected malformed: %q", raw)
		}
	}
}
