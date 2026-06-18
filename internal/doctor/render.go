package doctor

import (
	"encoding/json"
	"fmt"
	"io"
)

func symbol(s Status) string {
	switch s {
	case StatusOK:
		return "[ ok ]"
	case StatusWarn:
		return "[warn]"
	case StatusFail:
		return "[fail]"
	default:
		return "[skip]"
	}
}

// Render writes a human checklist. Every check appears on its own line.
func Render(w io.Writer, r Result) {
	for _, c := range r.Checks {
		if c.Detail != "" {
			fmt.Fprintf(w, "%s %-16s %s\n", symbol(c.Status), c.Name, c.Detail)
			continue
		}
		fmt.Fprintf(w, "%s %s\n", symbol(c.Status), c.Name)
	}
	if r.OK() {
		fmt.Fprintln(w, "\ndoctor: ok")
	} else {
		fmt.Fprintln(w, "\ndoctor: FAILED")
	}
}

// RenderJSON writes the result as indented JSON.
func RenderJSON(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
