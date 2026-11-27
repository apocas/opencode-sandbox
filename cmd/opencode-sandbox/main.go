// Command opencode-sandbox runs opencode in hardened sandboxes.
package main

import (
	"os"

	"github.com/apocas/opencode-sandbox/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
