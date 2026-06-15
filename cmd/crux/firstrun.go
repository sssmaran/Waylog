package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/firstrun"
)

// runFirstRun parses `crux first-run [--requests N] [--timeout DUR]` and runs it.
func runFirstRun(args []string) int {
	opt := firstrun.Options{Stdout: os.Stdout, Stderr: os.Stderr}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--requests":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage: crux first-run [--requests N] [--timeout DUR]")
				return 2
			}
			if _, err := fmt.Sscanf(args[i+1], "%d", &opt.Requests); err != nil {
				fmt.Fprintf(os.Stderr, "invalid --requests: %v\n", err)
				return 2
			}
			i++
		case "--timeout":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage: crux first-run [--requests N] [--timeout DUR]")
				return 2
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --timeout: %v\n", err)
				return 2
			}
			opt.Timeout = d
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[i])
			return 2
		}
	}
	if err := firstrun.Run(opt); err != nil {
		fmt.Fprintf(os.Stderr, "first-run failed: %v\n", err)
		return 1
	}
	return 0
}
