// Command opencode-sandbox runs opencode in hardened sandboxes (containers).
// Each sandbox may use at most ONE SSH key, chosen when it is created: an
// existing key pair from ~/.ssh, a new disposable key, or none. A private
// key never enters a sandbox where a host ssh-agent can serve it. Folders are
// copied into a sandbox, never mounted. See opencode-sandbox --help.
package main

import (
	"os"

	"github.com/apocas/opencode-sandbox/internal/app"
)

func main() { os.Exit(app.Main(os.Args[1:])) }
