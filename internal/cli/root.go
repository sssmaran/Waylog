package cli

import "fmt"

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

func usage() {
	fmt.Println("usage:")
	fmt.Println("  waylog graph failures [--tier=premium]")
}
