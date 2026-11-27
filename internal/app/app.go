// Package app is the opencode-sandbox command: it parses the command line and
// runs each command. Its behaviour matches the bash launcher (0.1–0.4) it
// replaces, so sandboxes created by either can be managed by both.
package app

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	assets "github.com/apocas/opencode-sandbox"
	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/backend/apple"
	"github.com/apocas/opencode-sandbox/internal/backend/docker"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
	"github.com/apocas/opencode-sandbox/internal/tty"
)

// Version is this release; images are tagged with it, so after an update the
// next run builds the matching image by itself.
const Version = "0.5.0"

// pidsLimit is the most processes a sandbox may run.
const pidsLimit = 1024

// hangupCleanup is the hidden command a closed terminal leaves behind to
// stop the sandbox (see open).
const hangupCleanup = "--internal-hangup-cleanup"

type app struct {
	r run.Runner
	// engines are the engines found running, docker first; sandboxes may
	// live in any of them.
	engines []backend.Engine
	docker  *docker.Docker
	// dockerInfo describes Docker, if it's running.
	dockerInfo backend.Info
	// newE is the engine new sandboxes go to; nil if none can (newErr says
	// why).
	newE   backend.Engine
	newErr error
	agents sshagent.Agents
	// plat is how new sandboxes run.
	plat platform.Platform
	// tty: stdin and stdout are terminals, so sandboxes get one too.
	tty bool
	// atExit runs however the command ends, like bash's EXIT trap.
	atExit []func()
	// keyDirsCache holds keyDirs once looked up.
	keyDirsCache []string
}

// Main runs the command line args and returns the exit status.
func Main(args []string) int {
	var r run.Runner = run.OS{}
	if os.Getenv("OPENCODE_SANDBOX_DEBUG") != "" {
		r = run.OS{Debug: os.Stderr}
	}
	a := &app{
		r:      r,
		docker: &docker.Docker{R: r},
		tty:    tty.IsTerminal(os.Stdin.Fd()) && tty.IsTerminal(os.Stdout.Fd()),
	}
	// Only Docker sandboxes can have a host ssh-agent.
	a.agents = sshagent.Agents{R: r, State: func(name string) string { return a.docker.State(state.Prefix + name) }}

	code, err := a.main(args)
	for i := len(a.atExit) - 1; i >= 0; i-- {
		a.atExit[i]()
	}
	var ee *run.ExitError
	switch {
	case err == nil:
		return code
	case errors.As(err, &ee):
		return ee.Code // the program that failed already said why
	default:
		fmt.Fprintf(os.Stderr, "opencode-sandbox: %v\n", err)
		return 1
	}
}

func (a *app) main(args []string) (int, error) {
	first := arg(args, 0)
	switch first {
	case "-h", "--help":
		a.usage(os.Stdout)
		return 0, nil
	case "-V", "--version":
		fmt.Printf("opencode-sandbox %s (opencode %s)\n", Version, opencodeVersion())
		return 0, nil
	case hangupCleanup:
		a.hangupCleanup(arg(args, 1))
		return 0, nil
	case "--health":
		// before the platform check: it reports what that check refuses
		f := health.Gather(a.r, a.docker, &apple.Apple{R: a.r}, Version, image())
		f.PidsLimit = pidsLimit
		if !health.Report(os.Stdout, f) {
			return 1, nil
		}
		return 0, nil
	}

	if err := a.setup(); err != nil {
		return 0, err
	}

	switch first {
	case "--build":
		if a.newE == nil {
			return 0, a.newErr
		}
		return 0, a.build(a.newE, args[1:])
	case "--list":
		a.agents.GC()
		return 0, a.list(os.Stdout, false)
	case "--rm":
		return 0, a.remove(args[1:])
	case "--cp":
		return 0, a.copyIn(args[1:])
	case "--get":
		return 0, a.copyOut(args[1:])
	case "--status":
		a.status()
		return 0, nil
	case "--stop-agent":
		a.stopAgents()
		return 0, nil
	case "--pubkey":
		return 0, a.pubkey(arg(args, 1))
	case "--shell":
		return 0, a.shell(arg(args, 1))
	case "--resume":
		return a.resume(arg(args, 1))
	}
	if strings.HasPrefix(first, "-") && !oneOf(first, "-w", "-n", "-k", "-p", "--") {
		return 0, fmt.Errorf("unknown option: %s (see: opencode-sandbox --help)", first)
	}
	return a.create(args)
}

// findEngines finds the engines that are running.
func (a *app) findEngines() {
	if info, err := a.docker.Info(); err == nil {
		a.dockerInfo = info
		a.engines = append(a.engines, a.docker)
	}
	if apple.Available(a.r) {
		a.engines = append(a.engines, &apple.Apple{R: a.r})
	}
}

// setup refuses setups the launcher can't work on, finds the engines, and
// decides where and how new sandboxes run.
func (a *app) setup() error {
	if os.Getuid() == 0 {
		return errors.New(platform.RootRefusal)
	}
	a.findEngines()
	if len(a.engines) == 0 {
		if runtime.GOOS == "darwin" {
			return errors.New(platform.NoEngineMac)
		}
		return errors.New(platform.NoEngine)
	}
	name, err := platform.ChooseEngine(os.Getenv(platform.EngineEnv), a.engine("docker") != nil, a.engine("apple") != nil)
	if err != nil {
		a.newErr = err
		return nil // other commands still work
	}
	a.newE = a.engine(name)
	switch name {
	case "docker":
		a.plat = platform.Decide(a.dockerInfo, os.Getenv(platform.RuntimeEnv))
		if err := platform.CheckRuntime(a.dockerInfo, a.plat.Runtime); err != nil {
			a.newE, a.newErr = nil, err
		}
	case "apple":
		a.plat = platform.Platform{NoAgentWhy: platform.AppleWhy}
		if rt := os.Getenv(platform.RuntimeEnv); rt != "" {
			a.newE, a.newErr = nil, fmt.Errorf("%s=%s chooses a Docker runtime, but new sandboxes go to Apple container; set %s=docker to use Docker", platform.RuntimeEnv, rt, platform.EngineEnv)
		}
	}
	return nil
}

// engine is the running engine with that name, or nil.
func (a *app) engine(name string) backend.Engine {
	for _, e := range a.engines {
		if e.Name() == name {
			return e
		}
	}
	return nil
}

// engineOf is the engine that has sandbox name, or nil.
func (a *app) engineOf(name string) backend.Engine {
	for _, e := range a.engines {
		if e.Exists(state.Prefix + name) {
			return e
		}
	}
	return nil
}

// rows lists the sandboxes of every engine.
func (a *app) rows() ([]backend.Row, error) {
	var all []backend.Row
	for _, e := range a.engines {
		rows, err := e.Rows(state.LabelName)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}

func (a *app) note(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "opencode-sandbox: "+format+"\n", args...)
}

// image is the image new sandboxes use.
func image() string { return "opencode-sandbox:" + Version }

// opencodeVersion is the opencode release the image bundles.
func opencodeVersion() string {
	df, _ := assets.Files.ReadFile("Dockerfile")
	sc := bufio.NewScanner(bytes.NewReader(df))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ARG OPENCODE_VERSION="); ok {
			return v
		}
	}
	return "unknown"
}

// build (re)builds the image in engine e from the embedded Dockerfile.
func (a *app) build(e backend.Engine, extra []string) error {
	dir, err := os.MkdirTemp("", "opencode-sandbox-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, mode := range map[string]os.FileMode{"Dockerfile": 0o644, "entrypoint.sh": 0o755, "ssh_config": 0o644} {
		data, err := assets.Files.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return err
		}
	}
	return e.Build(image(), dir, [][2]string{
		{"UID", strconv.Itoa(os.Getuid())},
		{"GID", strconv.Itoa(os.Getgid())},
		{"OPENCODE_SANDBOX_VERSION", Version},
	}, extra)
}

func (a *app) usage(w io.Writer) {
	fmt.Fprintf(w, `Usage: opencode-sandbox [-w DIR] [-n NAME] [-k KEY] [-p PROVIDER] [-- ARGS...]
       opencode-sandbox COMMAND

Run opencode in a hardened sandbox: a container of Docker, or on a Mac a VM of
its own in Apple container. Without a command, this creates a NEW sandbox
every time, with a copy of DIR, and opens opencode in it.

Options for a new sandbox:
  -w DIR          folder to copy into the sandbox's /workspace (default: the
                  current folder). It is copied, never mounted, so nothing the
                  agent does reaches DIR. Your home folder, ~/.ssh and
                  opencode-sandbox's own data are refused.
  -n NAME         sandbox name (default: the folder's name plus a random suffix)
  -k KEY          the one SSH key the sandbox may use (asked if omitted):
                    NAME  a key pair from ~/.ssh, e.g. id_ed25519
                    new   a new disposable key, deleted with the sandbox; its
                          public part is shown so you can authorize it on
                          servers. Kept in:
                          %s
                    none  no SSH access
                  Where a sandbox can't reach an ssh-agent on this machine
                  (macOS, Docker Desktop, rootless Docker, gVisor without
                  --host-uds), only "new" and "none" are offered, and the new
                  key's private part is stored inside the sandbox, where the
                  agent can read it.
  -p PROVIDER     the opencode provider(s) the sandbox may use, from your
                  opencode config; several as a,b (asked if omitted, or picked
                  automatically when you have only one). The sandbox gets a
                  config with only those providers and their API keys;
                  opencode's built-in providers are switched off.
  -- ARGS         pass the remaining arguments to opencode

Commands:
  --list          list sandboxes
  --resume [NAME] reopen a sandbox and continue its last opencode session
                  (shows a numbered list to pick from if NAME is omitted)
  --pubkey NAME   print the public key a sandbox uses
  --shell NAME    open bash inside a sandbox that is currently open
  --cp NAME PATH...
                  copy files or folders from this machine into a sandbox's
                  /workspace, open or closed (same checks as -w). opencode sees
                  them right away; no restart needed
  --get NAME PATH...
                  copy files or folders out of a sandbox (paths relative to its
                  /workspace) into the current folder; never overwrites
  --rm NAME...    delete sandboxes, their files and their disposable keys
  --rm --all      delete every sandbox (asks first)
  --status        show the ssh-agents of open sandboxes and their keys
  --stop-agent    stop all sandbox ssh-agents (cuts SSH for open sandboxes)
  --health        check that sandboxes can run here, show how they are
                  isolated, and what you could install to isolate them better
  --build [ARGS]  rebuild the image (extra ARGS go to docker build);
                  existing sandboxes keep the image they were created with
  -V, --version   show the opencode-sandbox and opencode versions
  -h, --help      show this help

Environment:
  OPENCODE_SANDBOX_CONFIG      opencode config to take providers from
                               (default: ~/.config/opencode/opencode.json)
  OPENCODE_SANDBOX_NO_AGENT    set to 1 to use the no-agent mode above even
                               where a host ssh-agent would work
  OPENCODE_SANDBOX_SEED_HOSTS  hosts (e.g. a jump host) whose keys are copied
                               from your ~/.ssh/known_hosts into sandboxes so
                               the first hop is verified; space-separated,
                               e.g. "[jump.example.com]:2222 bastion.lan"
  OPENCODE_SANDBOX_ENGINE      docker or apple: where new sandboxes go
                               (default: Apple container where it runs, a VM
                               for each sandbox; else Docker)
  OPENCODE_SANDBOX_RUNTIME     a Docker runtime for new sandboxes, e.g. runsc
                               for gVisor (see --health)
  OPENCODE_SANDBOX_DEBUG       set to 1 to print every command run

Inside a sandbox:
  Quitting opencode or closing the terminal stops the sandbox and unloads its
  key. Press ctrl-] ctrl-] to detach and leave it running; --resume reattaches
  (not in Apple container, which can't detach or reattach).
  Keys from ~/.ssh never enter a sandbox: where a host ssh-agent works, a
  sandbox uses its one key through a dedicated agent and cannot read or copy
  it.

Examples:
  opencode-sandbox -w ./workdir                    new sandbox, pick a key
  opencode-sandbox -w ./workdir -n prod -k id_ed25519 -p my-provider
  opencode-sandbox -w ./workdir -k new             with a disposable key
  opencode-sandbox --resume prod                   continue where you left off
`, state.KeysDir())
}

// --- small helpers ---------------------------------------------------------

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func oneOf(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

var number = regexp.MustCompile(`^[1-9][0-9]*$`)

// choice parses a menu answer between 1 and max.
func choice(s string, max int) (int, bool) {
	if !number.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n <= max
}

// splitList splits a comma-separated list the way bash's
// `IFS=, read -ra` does: a final empty field is dropped, so "a," is one item,
// "," is one empty item and "" is none.
func splitList(s string) []string {
	f := strings.Split(s, ",")
	if f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	return f
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// present is true for anything at p, even a dangling symlink.
func present(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}
