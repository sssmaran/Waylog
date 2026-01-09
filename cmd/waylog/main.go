package main

import (
	"os"

	"github.com/sssmaran/WaylogCLI/internal/cli"
)

func main() {
	cli.Run(os.Args[1:])
}
