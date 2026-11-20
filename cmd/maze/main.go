package main

import (
	"errors"
	"os"

	"github.com/packagemaze/maze-cli/internal/cli"
)

func main() {
	if err := cli.NewRootCommand(cli.DefaultDependencies()).Execute(); err != nil {
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
