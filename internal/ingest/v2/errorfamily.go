package ingestv2

import (
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func FormatErrorFamily(f ErrorFamily) string {
	return apiv2.FormatErrorFamily(f)
}

func FormatEventErrorFamily(ev *eventv2.Event) *string {
	if ev == nil || ev.Anchor == nil {
		return nil
	}
	s := FormatErrorFamily(ErrorFamily{Service: ev.Service, Step: ev.Anchor.Step, ErrorCode: ev.Anchor.ErrorCode})
	return &s
}

func ParseErrorFamily(s string) (BlastKey, bool) {
	return apiv2.ParseErrorFamily(s)
}
