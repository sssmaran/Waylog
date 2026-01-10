package cli

import (
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph"
)

func Run(args []string) {
	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "graph":
		runGraph(args[1:])
	default:
		usage()
	}
}

var store *graph.Store

func SetStore(s *graph.Store) {
	store = s
}

func usage() {
	fmt.Println("usage:")
	fmt.Println("  waylog graph failures [--tier=premium]")
}
