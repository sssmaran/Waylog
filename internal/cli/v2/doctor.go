package cliv2

import (
	"fmt"
	"io"

	"github.com/sssmaran/WaylogCLI/internal/doctor"
)

// runDoctor runs read-only local checks (always) plus server reachability checks
// when --server is passed. Server checks probe cfg.addr (default localhost:8080).
func runDoctor(cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	server := false
	for _, a := range args {
		switch a {
		case "--server":
			server = true
		default:
			return usage(stderr, "usage: waylog doctor [--server] [--json]")
		}
	}

	res := doctor.Run(doctor.Options{Addr: cfg.addr, ServerChecks: server})

	if cfg.json {
		if err := doctor.RenderJSON(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	} else {
		doctor.Render(stdout, res)
	}
	if res.OK() {
		return 0
	}
	return 1
}
