// Package core does what opencode-sandbox does: it creates, opens, copies
// into and out of, and deletes sandboxes, in whichever engines run here.
// It asks nothing and prints nothing itself: the command line (package cli)
// and the dashboard (package tui) decide, and show what it reports.
package core

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
)

// Version is this release; images are tagged with it, so after an update the
// next run builds the matching image by itself.
const Version = "0.12.0"

// pidsLimit is the most processes a sandbox may run.
const pidsLimit = 1024

// HangupCleanupArg is the hidden command a closed terminal leaves behind to
// stop the sandbox (see Open).
const HangupCleanupArg = "--internal-hangup-cleanup"

// Reporter is told what Core is doing, for the user to see.
type Reporter interface {
	// Note and Warn are messages; Warn is something to act on.
	Note(msg string)
	Warn(msg string)
	// Step starts a step that may take a while (the previous one is done);
	// Done ends the last one.
	Step(title string)
	Done()
	// Log receives the raw output of long steps, such as image builds.
	Log() io.Writer
}

// Core is opencode-sandbox on this machine.
type Core struct {
	r run.Runner
	// Rep is told what's going on; by default, notes go to stderr.
	Rep Reporter
	// TTY: sandboxes are opened on a terminal, so they get one too.
	TTY bool
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
	// keyDirsCache holds keyDirs once looked up.
	keyDirsCache []string
	// cfgRoot is your opencode config as of cfgStamp (see config).
	cfgRoot  any
	cfgStamp string
}

// New makes a Core; OPENCODE_SANDBOX_DEBUG prints every command it runs.
func New() *Core {
	var r run.Runner = run.OS{}
	if os.Getenv("OPENCODE_SANDBOX_DEBUG") != "" {
		r = run.OS{Debug: os.Stderr}
	}
	a := &Core{r: r, docker: &docker.Docker{R: r}, Rep: Plain{}}
	// Only Docker sandboxes can have a host ssh-agent.
	a.agents = sshagent.Agents{R: r, State: func(name string) string { return a.docker.State(state.Prefix + name) }}
	return a
}

// Plain is a Reporter that writes notes to stderr and drops the rest.
type Plain struct{}

func (Plain) Note(msg string) { fmt.Fprintln(os.Stderr, "opencode-sandbox: "+msg) }
func (Plain) Warn(msg string) { fmt.Fprintln(os.Stderr, "opencode-sandbox: "+msg) }
func (Plain) Step(string)     {}
func (Plain) Done()           {}
func (Plain) Log() io.Writer  { return io.Discard }

func (a *Core) note(format string, args ...any) { a.Rep.Note(fmt.Sprintf(format, args...)) }

// findEngines finds the engines that are running.
func (a *Core) findEngines() {
	a.engines = nil
	if info, err := a.docker.Info(); err == nil {
		a.dockerInfo = platform.CheckRuntimes(info)
		a.engines = append(a.engines, a.docker)
	}
	if apple.Available(a.r) {
		a.engines = append(a.engines, &apple.Apple{R: a.r})
	}
}

// Setup refuses setups opencode-sandbox can't work on, finds the engines,
// and decides where and how new sandboxes run.
func (a *Core) Setup() error {
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
	// Other commands still work where new sandboxes can't be made.
	a.newE, a.plat, a.newErr = a.place("", "")
	return nil
}

// place decides where a new sandbox runs: in the engine (docker or apple)
// and, in Docker, on the runtime asked for. "" leaves each to the
// environment (EngineEnv, RuntimeEnv), and that to what isolates best here.
// The error is why a sandbox can't be made there.
func (a *Core) place(engine, rt string) (backend.Engine, platform.Platform, error) {
	// An error names what asked: the option, or else the variable.
	engineBy, rtBy := "", ""
	if engine != "" {
		if engine != "docker" && engine != "apple" {
			return nil, platform.Platform{}, fmt.Errorf("--engine must be docker or apple, not %q", engine)
		}
		engineBy = "--engine " + engine
	} else {
		engine = os.Getenv(platform.EngineEnv)
	}
	if rt != "" {
		rtBy = "--runtime " + rt
	} else {
		rt = os.Getenv(platform.RuntimeEnv)
	}
	said := func(err error, env, v, by string) error {
		if err == nil || by == "" {
			return err
		}
		return errors.New(strings.Replace(err.Error(), env+"="+v, by, 1))
	}
	name, err := platform.ChooseEngine(engine, a.engine("docker") != nil, a.engine("apple") != nil)
	if err != nil {
		return nil, platform.Platform{}, said(err, platform.EngineEnv, engine, engineBy)
	}
	if name == "docker" {
		chosen, _ := platform.ChooseRuntime(runtime.GOOS, a.dockerInfo, rt)
		plat := platform.Decide(a.dockerInfo, chosen)
		if err := platform.CheckRuntime(a.dockerInfo, plat.Runtime); err != nil {
			return nil, plat, said(err, platform.RuntimeEnv, rt, rtBy)
		}
		return a.engine(name), plat, nil
	}
	plat := platform.Platform{NoAgentWhy: platform.AppleWhy}
	switch {
	case rt != "" && rtBy != "":
		return nil, plat, fmt.Errorf("%s chooses a Docker runtime, but the sandbox would go to Apple container; add --engine docker", rtBy)
	case rt != "":
		return nil, plat, fmt.Errorf("%s=%s chooses a Docker runtime, but new sandboxes go to Apple container; set %s=docker to use Docker", platform.RuntimeEnv, rt, platform.EngineEnv)
	}
	return a.engine(name), plat, nil
}

// CheckPlace says why a new sandbox can't run in that engine and on that
// runtime ("" for each leaves it to the environment, then to what's best
// here); nil if it can.
func (a *Core) CheckPlace(engine, rt string) error {
	_, _, err := a.place(engine, rt)
	return err
}

// Place is somewhere a new sandbox can run here.
type Place struct {
	// Engine and Runtime are what CreateOptions takes; both "" for wherever
	// new sandboxes go by themselves.
	Engine, Runtime string
	Label, Detail   string
}

// Places lists where a new sandbox can run here: by itself first, then each
// engine that's running and, in Docker, each kind of runtime it has.
func (a *Core) Places() []Place {
	isolation := func(e backend.Engine, plat platform.Platform) (string, string) {
		if e.Name() == "apple" {
			return "Apple container", "a VM of its own"
		}
		rt := plat.Runtime
		if rt == "" {
			rt = a.dockerInfo.DefaultRuntime
		}
		switch platform.Kind(a.dockerInfo, rt) {
		case platform.Gvisor:
			return "Docker with gVisor", "a kernel of its own (runtime " + rt + ")"
		case platform.Kata:
			return "Docker with Kata", "a VM of its own (runtime " + rt + ")"
		}
		if platform.InVM(a.dockerInfo) {
			return "Docker", "a hardened container in the VM your containers share (runtime " + rt + ")"
		}
		return "Docker", "a hardened container on this machine's kernel (runtime " + rt + ")"
	}
	var out []Place
	seen := map[string]bool{}
	add := func(engine, rt string, auto bool) {
		e, plat, err := a.place(engine, rt)
		if err != nil {
			return
		}
		label, detail := isolation(e, plat)
		if auto {
			out = append(out, Place{Label: "automatic", Detail: label + ": " + detail})
			return
		}
		if !seen[label+detail] {
			seen[label+detail] = true
			out = append(out, Place{Engine: engine, Runtime: rt, Label: label, Detail: detail})
		}
	}
	add("", "", true)
	if a.engine("apple") != nil {
		add("apple", "", false)
	}
	if a.engine("docker") != nil {
		// One runtime of each kind: the one new sandboxes would get, then an
		// ordinary one, then the others by name.
		best, _ := platform.ChooseRuntime(runtime.GOOS, a.dockerInfo, "")
		names := []string{best, a.dockerInfo.DefaultRuntime, "runc"}
		var rest []string
		for name := range a.dockerInfo.Runtimes {
			rest = append(rest, name)
		}
		sort.Strings(rest)
		kinds := map[string]bool{}
		for _, name := range append(names, rest...) {
			rt, ok := a.dockerInfo.Runtimes[name]
			if k := platform.Kind(a.dockerInfo, name); name != "" && ok && !rt.Missing && !kinds[k] {
				kinds[k] = true
				add("docker", name, false)
			}
		}
	}
	return out
}

// Engine is a running engine as the user sees it.
type Engine struct {
	Name    string // "Docker" or "Apple container"
	Version string
	New     bool // new sandboxes go here
	// With is what new sandboxes run on in it, where that's more than the
	// engine itself: "gVisor" or "Kata".
	With string
}

// Engines lists the running engines.
func (a *Core) Engines() []Engine {
	var es []Engine
	for _, e := range a.engines {
		info := a.dockerInfo
		name := "Docker"
		if e.Name() == "apple" {
			name = "Apple container"
			info, _ = e.Info()
		}
		with := ""
		if e == a.newE && e.Name() == "docker" {
			with = map[string]string{platform.Gvisor: "gVisor", platform.Kata: "Kata"}[platform.Kind(a.dockerInfo, a.runtime())]
		}
		es = append(es, Engine{Name: name, Version: info.Version, New: e == a.newE, With: with})
	}
	return es
}

// CanCreate says why new sandboxes can't be made here; nil if they can.
func (a *Core) CanCreate() error {
	if a.newE == nil {
		return a.newErr
	}
	return nil
}

// engine is the running engine with that name, or nil.
func (a *Core) engine(name string) backend.Engine {
	for _, e := range a.engines {
		if e.Name() == name {
			return e
		}
	}
	return nil
}

// engineOf is the engine that has sandbox name, or nil.
func (a *Core) engineOf(name string) backend.Engine {
	for _, e := range a.engines {
		if e.Exists(state.Prefix + name) {
			return e
		}
	}
	return nil
}

// Exists says whether there's a sandbox by that name.
func (a *Core) Exists(name string) bool { return a.engineOf(name) != nil }

// rows lists the sandboxes of every engine.
func (a *Core) rows() ([]backend.Row, error) {
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

// image is the image new sandboxes use.
func image() string { return "opencode-sandbox:" + Version }

// OpencodeVersion is the opencode release the image bundles.
func OpencodeVersion() string {
	df, _ := assets.Files.ReadFile("Dockerfile")
	sc := bufio.NewScanner(bytes.NewReader(df))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ARG OPENCODE_VERSION="); ok {
			return v
		}
	}
	return "unknown"
}

// Build (re)builds the image for new sandboxes from the embedded
// Dockerfile; extra arguments go to the engine's build command.
func (a *Core) Build(extra []string) error {
	if err := a.CanCreate(); err != nil {
		return err
	}
	return a.build(a.newE, extra)
}

func (a *Core) build(e backend.Engine, extra []string) error {
	dir, err := os.MkdirTemp("", "opencode-sandbox-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, mode := range map[string]os.FileMode{"Dockerfile": 0o644, "entrypoint.sh": 0o755, "gateway.pl": 0o755, "ssh_config": 0o644} {
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
	}, extra, a.Rep.Log())
}

// Health finds out what --health reports. It works before Setup, and when
// Setup fails.
func (a *Core) Health() health.Facts {
	ap := &apple.Apple{R: a.r}
	f := health.Gather(a.r, a.docker, ap, Version, image())
	f.PidsLimit = pidsLimit
	var info backend.Info
	switch {
	case f.Engine == "docker" && f.Docker != nil:
		info = *f.Docker
	case f.Engine == "apple":
		info, _ = ap.Info()
	}
	if f.Engine != "" {
		if l, _, err := limitsFor(f.Engine, info, "", ""); err != nil {
			f.LimitsErr = err.Error()
		} else {
			f.Limits = l.String()
		}
	}
	return f
}

// --- small helpers ---------------------------------------------------------

func oneOf(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
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
