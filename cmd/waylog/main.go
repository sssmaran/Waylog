package main

import (
	"os"

	cliv2 "github.com/sssmaran/WaylogCLI/internal/cli/v2"
)

func main() {
	os.Exit(cliv2.RunCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
