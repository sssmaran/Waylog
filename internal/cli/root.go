package cli

import (
	"fmt"

	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
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

var store *graphstore.Store

func SetStore(s *graphstore.Store) {
	store = s
}

func usage() {
	fmt.Println("usage:")
	fmt.Println("  waylog graph failures [--tier=premium]")
}
