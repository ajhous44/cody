package main

import (
	"os"

	"github.com/ajhous44/cody/v3/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
