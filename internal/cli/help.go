package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/apocas/opencode-sandbox/internal/state"
)

// termWidth is stdout's width, or 80.
func termWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	return 80
}

type command struct{ name, args, short, long string }

var commands = []command{
	{"new", "[DIR] [-n NAME] [-k KEY] [-p PROVIDER] [OPTIONS] [-- ARGS]", "create a sandbox with a copy of DIR and open it", `
Creates a NEW sandbox with a copy of DIR (default: the current folder) in
/workspace, and opens opencode in it. DIR is copied, never mounted, so nothing
the agent does reaches it. Your home folder, ~/.ssh and opencode-sandbox's own
data are refused. Dot files in DIR that may hold secrets (.env, .npmrc…) are
copied like the rest: they're named first, and on a terminal you're asked.

  -n NAME      sandbox name (default: the folder's name plus a random suffix)
  -k KEY       the one SSH key the sandbox may use (asked if omitted):
                 NAME  a key pair from ~/.ssh, e.g. id_ed25519
                 new   a new disposable key, deleted with the sandbox; its
                       public part is shown so you can authorize it. Kept in
                       {{keys}}
                 none  no SSH access
               Where a sandbox can't reach an ssh-agent on this machine (macOS,
               Docker Desktop, rootless Docker, gVisor without --host-uds),
               only "new" and "none" are offered, and the new key's private
               part is stored inside the sandbox, where the agent can read it.
  -p PROVIDER  the opencode provider(s) the sandbox may use, from your
               opencode config; several as a,b (asked if omitted, automatic
               if you have only one). The sandbox gets a config with only
               those providers; opencode's built-in providers are switched
               off. Their API keys stay on this machine: opencode reaches
               each provider through a gateway that puts its key in. So do
               the headers and addresses of your remote MCP servers.
  --empty      copy nothing: the sandbox starts with an empty /workspace
               (instead of DIR)
  --network open|filtered|none
               open (the default): the internet, your network and services on
               this machine. filtered: only its providers and the hosts on
               its list (--allow, and later: opencode-sandbox allow); what
               it's refused is written down (opencode-sandbox net). none: no
               network at all; the sandbox reaches only its providers,
               through the gateway, so it gets no SSH key and can't download
               anything.
  --allow HOSTS
               what a sandbox with a filtered network may reach from the
               start, as a,b (implies --network filtered): hosts
               (example.com, *.example.com, 192.0.2.7, 10.0.0.0/8, each for
               ports 443, 80 and 22, or with :PORT for one port) and the
               built-in lists github, gitlab, npm, pypi and go
  --engine docker|apple
               where it runs (default: Apple container where that runs, else
               Docker)
  --runtime NAME
               the Docker runtime it runs on, e.g. runc (default: gVisor where
               Docker has it)
  --gateway on|off
               on (the default): its providers' API keys stay on this machine.
               off: they go into its config.
  --memory AMOUNT
               the most memory it may use, like 8g or 512m (default: 4g). Over
               that, what runs in it is stopped; this machine isn't starved.
  --cpus N     the most CPUs it may use (default: 4)
  -- ARGS      pass the remaining arguments to opencode

A sandbox never gets more memory or CPUs than its engine has. The defaults of
the last six options can be set in the environment (see opencode-sandbox
help): OPENCODE_SANDBOX_NETWORK, _ENGINE, _RUNTIME, _GATEWAY, _MEMORY, _CPUS.`},
	{"ls", "", "list sandboxes", `
Lists sandboxes: their state, what isolates them (docker, gvisor or apple),
their SSH key (* when its private part is stored inside), providers (* when
an API key or an MCP server's secret is stored inside), age and the folder
they were copied from.`},
	{"open", "[NAME]", "reopen a sandbox, continuing its last opencode session", `
Reopens a sandbox and continues its last opencode session, or attaches to it
if it's open in another terminal (not in Apple container). Without NAME, asks
which.`},
	{"shell", "NAME", "open bash inside a sandbox that is open", ""},
	{"key", "NAME", "print the public key a sandbox uses", ""},
	{"cp", "NAME PATH...", "copy files or folders into a sandbox's /workspace", `
Copies files or folders from this machine into a sandbox's /workspace, open or
closed (same checks as new). opencode sees them right away.`},
	{"get", "NAME PATH...", "copy files or folders out of a sandbox", `
Copies files or folders out of a sandbox (paths relative to its /workspace)
into the current folder. Never overwrites; what comes out was written by the
agent, so treat it as untrusted.`},
	{"rm", "NAME... | --all [-y]", "delete sandboxes, their files and disposable keys", `
Deletes sandboxes with their files and disposable keys. --all deletes every
sandbox after asking (-y: without asking).`},
	{"net", "NAME", "show the hosts a sandbox may reach, and what it was refused", `
For a sandbox with a filtered network (new --network filtered): the hosts
and built-in lists it may reach besides its providers, what it's denied, and
the hosts it tried to reach and was refused, with how often and why.`},
	{"allow", "NAME HOST...", "let a sandbox with a filtered network reach hosts", `
Puts hosts on the list a sandbox with a filtered network may reach. It
applies at once, also while the sandbox is open.

  HOST         example.com          that host
               *.example.com        every host under that domain
               192.0.2.7            an address
               10.0.0.0/8           a range of addresses
               example.com:8443     with :PORT, that port only; without,
                                    ports 443, 80 and 22 (HTTPS, HTTP, SSH)
               github, gitlab, npm, pypi, go
                                    a built-in list: what that tool needs

This machine, your network and a cloud's metadata address are reached only by
a rule that names their address; a name that leads there isn't enough. An
allowed host is a way out of the sandbox for whoever has an account on it.`},
	{"deny", "NAME HOST...", "refuse a sandbox with a filtered network hosts", `
Puts hosts on the list a sandbox with a filtered network is denied, which
wins over what's allowed (deny one host under an allowed *.example.com, say),
and takes them off the allowed ones. With the name of a built-in list, takes
that list off.`},
	{"health", "", "check that sandboxes can run here and how they're isolated", `
Checks that sandboxes can run here, shows how a new one is isolated, and what
you could install to isolate sandboxes better. Exits 1 if they can't run.`},
	{"setup", "[-y]", "install the strongest isolation this machine can have", `
Sets this machine up so that new sandboxes are isolated as strongly as it
allows. It shows what it will do, command by command, and asks first:

  on a Mac     installs and starts Apple container (macOS 26 or later on
               Apple silicon): a lightweight VM for each sandbox
  on Linux     installs gVisor and registers it with Docker: a user-space
               kernel for each sandbox, with a runtime that may connect to
               the ssh-agent socket a sandbox is given

What it downloads comes from Apple's and gVisor's own releases and is checked
before use: Apple's signature on its installer, gVisor's published SHA-512.
The steps that need root run with sudo. Sandboxes you already have aren't
changed; new ones use what was set up by themselves. Where there's nothing
stronger to install, it says so. "opencode-sandbox health" shows what a new
sandbox gets now.

  -y           go ahead without asking`},
	{"build", "[ARGS]", "rebuild the image for new sandboxes", `
Rebuilds the image new sandboxes use; ARGS go to docker build or container
build. Existing sandboxes keep the image they were created with.`},
	{"agents", "[stop]", "show (or stop) the ssh-agents of open sandboxes", `
Shows the ssh-agents of open sandboxes and the key each holds; "stop" stops
them all, cutting SSH for open sandboxes.`},
	{"update", "[--check]", "update opencode-sandbox to the latest release", `
Updates opencode-sandbox itself: downloads the latest release's program for
this system from GitHub and, before it replaces the one you're running,
checks it against the release's SHA256SUMS, that the release's build
attestation says this project's release workflow built it, and that it runs. Where opencode-sandbox runs from
a git clone, it updates the clone with git pull --ff-only and rebuilds it.

  --check      only say whether there's something newer

Your sandboxes aren't touched: they keep the image they were created with.`},
	{"help", "[COMMAND]", "show help", ""},
	{"version", "", "show the opencode-sandbox and opencode versions", ""},
}

func (x *cli) help(w io.Writer, cmd string) {
	t := x.t
	h := func(s string) string { return t.Title.Render(s) }
	var b strings.Builder
	if cmd != "" {
		for _, c := range commands {
			if c.name == cmd {
				fmt.Fprintf(&b, "%s opencode-sandbox %s %s\n\n%s\n", h("Usage:"), t.Key.Render(c.name), c.args, c.short)
				if c.long != "" {
					b.WriteString(strings.ReplaceAll(c.long, "{{keys}}", state.KeysDir()) + "\n")
				}
				lipgloss.Fprint(w, b.String())
				return
			}
		}
	}
	fmt.Fprintf(&b, "%s opencode-sandbox [COMMAND]\n\n", h("Usage:"))
	b.WriteString(`Run opencode in hardened sandboxes: Docker containers, or on a Mac a VM for
each sandbox with Apple container. Without a command, on a terminal, this opens
the dashboard: your sandboxes, and keys to create, open, copy and delete them.

`)
	b.WriteString(h("Commands:") + "\n")
	for _, c := range commands {
		left := c.name
		if c.args != "" {
			if a := c.name + " " + c.args; len(a) <= 22 {
				left = a
			} else {
				left += " " + strings.Fields(c.args)[0] // new [DIR], rm NAME...
			}
		}
		fmt.Fprintf(&b, "  %s%s %s\n", t.Key.Render(left), strings.Repeat(" ", max(22-len(left), 1)), c.short)
	}
	b.WriteString(`
` + h("Environment:") + `
  OPENCODE_SANDBOX_CONFIG      opencode config to take providers from
                               (default: ~/.config/opencode/opencode.json)
  OPENCODE_SANDBOX_ENGINE      docker or apple: where new sandboxes go
                               (default: Apple container where it runs; else
                               Docker)
  OPENCODE_SANDBOX_RUNTIME     a Docker runtime for new sandboxes, e.g. runc
                               (default: gVisor where Docker has it; else
                               Docker's own default)
  OPENCODE_SANDBOX_NO_AGENT    1: keep keys inside sandboxes even where a host
                               ssh-agent would work
  OPENCODE_SANDBOX_GATEWAY     off: give new sandboxes their providers' API
                               keys, instead of a gateway that keeps them here
  OPENCODE_SANDBOX_NETWORK     none: give new sandboxes no network; they reach
                               only their providers, through the gateway.
                               filtered: one that also reaches the hosts on
                               a sandbox's list
  OPENCODE_SANDBOX_ASK         off: don't ask on screen (macOS) when a sandbox
                               with a filtered network wants a host that's on
                               no list; refuse it and write it down
  OPENCODE_SANDBOX_MEMORY      the most memory a new sandbox may use, like 8g
                               (default: 4g, or all the engine has if less)
  OPENCODE_SANDBOX_CPUS        the most CPUs a new sandbox may use (default: 4,
                               or all the engine has if fewer)
  OPENCODE_SANDBOX_SEED_HOSTS  hosts whose keys are copied from your
                               ~/.ssh/known_hosts into sandboxes, so the first
                               hop is verified; e.g. "[jump.example.com]:2222"
  OPENCODE_SANDBOX_INTRO       off: open the dashboard without its intro
  OPENCODE_SANDBOX_DEBUG       1: print every command run

` + h("Inside a sandbox:") + `
  Quitting opencode or closing the terminal stops the sandbox and unloads its
  key. Press ctrl-] ctrl-] to detach and leave it running; open reattaches (not
  in Apple container, which can't detach or reattach). Keys from ~/.ssh never
  enter a sandbox: where a host ssh-agent works, a sandbox uses its one key
  through a dedicated agent and cannot read or copy it.

` + h("Examples:") + `
  opencode-sandbox                              the dashboard
  opencode-sandbox new ~/src/api                new sandbox, pick key and provider
  opencode-sandbox new . -n api -k new -p my-provider
  opencode-sandbox open api                     continue where you left off
  opencode-sandbox help new                     all options of a command
`)
	lipgloss.Fprint(w, b.String())
}
