package cli

import (
	"fmt"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/netpolicy"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// words takes the hosts of a command line apart: each argument is one, or
// several with commas between them.
func words(args []string) []string {
	var out []string
	for _, a := range args {
		for _, w := range strings.Split(a, ",") {
			if w = strings.TrimSpace(w); w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

func (x *cli) allow(args []string) (int, error) {
	if len(args) < 2 {
		return 0, usagef("allow needs a sandbox name and the hosts to allow (see: opencode-sandbox help allow)")
	}
	name, ws := args[0], words(args[1:])
	if err := x.c.Allow(name, ws...); err != nil {
		return 0, err
	}
	for _, w := range ws {
		x.done(fmt.Sprintf("sandbox '%s' may reach %s", name, ui.NetWord(w)))
	}
	return 0, nil
}

func (x *cli) deny(args []string) (int, error) {
	if len(args) < 2 {
		return 0, usagef("deny needs a sandbox name and the hosts to deny (see: opencode-sandbox help deny)")
	}
	name, ws := args[0], words(args[1:])
	if err := x.c.Deny(name, ws...); err != nil {
		return 0, err
	}
	for _, w := range ws {
		if netpolicy.IsPreset(w) {
			x.done(fmt.Sprintf("sandbox '%s' no longer has the built-in list %s", name, w))
		} else {
			x.done(fmt.Sprintf("sandbox '%s' is denied %s, whatever else allows it", name, ui.NetWord(w)))
		}
	}
	return 0, nil
}

func (x *cli) net(args []string) (int, error) {
	if len(args) != 1 {
		return 0, usagef("net needs one sandbox name (see: opencode-sandbox ls)")
	}
	name := args[0]
	n, err := x.c.Net(name)
	if err != nil {
		return 0, err
	}
	dim := x.t.Dim.Render
	fmt.Printf("sandbox '%s' reaches its providers through the gateway, and:\n", name)
	if len(n.Policy.Presets)+len(n.Policy.Allow) == 0 {
		fmt.Println("  nothing else")
	}
	for _, p := range n.Policy.Presets {
		x.out("  " + ui.NetWord(p))
	}
	for _, r := range n.Policy.Allow {
		x.out("  " + ui.NetRule(r))
	}
	if len(n.Policy.Deny) > 0 {
		fmt.Println("\ndenied, whatever else allows it:")
		for _, r := range n.Policy.Deny {
			x.out("  " + ui.NetRule(r))
		}
	}
	if len(n.Blocked) == 0 {
		fmt.Println("\nnothing was refused")
		return 0, nil
	}
	fmt.Printf("\nrefused (%s):\n", ui.Plural(len(n.Blocked), "host"))
	for _, b := range n.Blocked {
		x.out(fmt.Sprintf("  %s  %s", ui.HostPort(b.Host, b.Port), dim(ui.Refused(b))))
	}
	x.out(dim(fmt.Sprintf("\nallow one with: opencode-sandbox allow %s HOST", name)))
	return 0, nil
}
