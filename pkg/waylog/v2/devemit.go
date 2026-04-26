package waylogv2

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type devLogRecord struct {
	service string
	traceID string
	level   string
	msg     string
	step    string
	fields  F
}

func (s *sdk) emitDevLog(rec devLogRecord) {
	if !s.devEnabled || s.devOut == nil {
		return
	}

	traceID := rec.traceID
	if len(traceID) > 8 {
		traceID = traceID[:8]
	}

	parts := []string{
		fmt.Sprintf("[%s]", strings.ToUpper(rec.level)),
		rec.service,
		traceID,
	}
	if rec.step != "" {
		parts = append(parts, rec.step)
	}
	parts = append(parts, rec.msg)

	for _, k := range sortedKeys(rec.fields) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, rec.fields[k]))
	}

	s.devMu.Lock()
	defer s.devMu.Unlock()
	_, _ = io.WriteString(s.devOut, strings.Join(parts, " ")+"\n")
}

func (s *sdk) emitDevFinal(ev *eventv2.Event) {
	if !s.devEnabled || s.devOut == nil || ev == nil {
		return
	}
	raw, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return
	}
	s.devMu.Lock()
	defer s.devMu.Unlock()
	_, _ = s.devOut.Write(append(raw, '\n'))
}

func sortedKeys(m F) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
