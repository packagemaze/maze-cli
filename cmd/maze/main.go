package main

import (
	"os"

	"github.com/packagemaze/maze-cli/internal/cli"
)

func main() {
	if err := cli.NewRootCommand(cli.DefaultDependencies()).Execute(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
