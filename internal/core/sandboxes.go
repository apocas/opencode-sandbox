package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// State is whether a sandbox is open.
type State int

const (
	StateNew    State = iota // created, never started
	StateOpen                // running
	StateClosed              // stopped
	StateOther               // anything else the engine says (dead, paused…)
)

// Sandbox is one sandbox as the user sees it.
type Sandbox struct {
	Name   string
	State  State
	Status string // "open (5 minutes)", "closed 2 hours ago", "new"…
	// Created is how long ago it was made: "2 hours ago".
	Created string
	Engine  string // "docker" or "apple"
	// RunsIn is what isolates it: "docker", "gvisor", "kata" or "apple".
	RunsIn  string
	KeyKind string // "home" (from ~/.ssh), "disposable" or "none"
	Key     string // the key's path, or "none"
	// KeyName is the key as shown: its file name, "disposable" or "none".
	KeyName string
	// KeyInside: the private key is stored inside the sandbox (no host
	// ssh-agent where it runs).
	KeyInside bool
	Providers string // "" for sandboxes from before provider selection
	// Gateway: its providers' API keys stay on this machine; without it
	// (sandboxes from before 0.9.0) they're in its config.
	Gateway bool
	// APIKeysInside: its config has an API key, or a secret of an MCP
	// server (see Core.keysInside).
	APIKeysInside bool
	// NoNetwork: it has no network, and reaches only its providers,
	// through the gateway.
	NoNetwork bool
	// Filtered: it has no network of its own, and reaches its providers and
	// the hosts on its list, through the gateway.
	Filtered bool
	// Limits are the memory and CPUs it may use (none in Docker before
	// 0.12.0).
	Limits  Limits
	Source  string // the folder it was copied from; "" if it started empty
	Version string // the opencode-sandbox that created it ("" before 0.1)
}

var (
	upRe        = regexp.MustCompile(`^Up (.*)$`)
	exitedRe    = regexp.MustCompile(`^Exited \([0-9-]+\) `)
	lessRe      = regexp.MustCompile(`Less than a second( ago)?`)
	aboutRe     = regexp.MustCompile(`About an? (minute|hour)`)
	justNowAgo  = regexp.MustCompile(`just now ago`)
	friendlyFix = func(s string) string {
		s = lessRe.ReplaceAllString(s, "just now")
		s = aboutRe.ReplaceAllString(s, "1 $1")
		return justNowAgo.ReplaceAllString(s, "just now")
	}
)

// friendlyState turns an engine's status into open/closed/new.
func friendlyState(s string) (State, string) {
	st := StateOther
	switch {
	case upRe.MatchString(s):
		st, s = StateOpen, upRe.ReplaceAllString(s, "open ($1)")
	case exitedRe.MatchString(s):
		st, s = StateClosed, exitedRe.ReplaceAllString(s, "closed ")
	case s == "Created":
		st, s = StateNew, "new"
	case s == "Exited":
		st, s = StateClosed, "closed"
	case s == "Up":
		st, s = StateOpen, "open"
	}
	return st, friendlyFix(s)
}

// CollectAgents stops ssh-agents left behind by sandboxes that closed
// without opencode-sandbox noticing. Not while a sandbox is being created:
// its agent starts before it does.
func (a *Core) CollectAgents() { a.agents.GC() }

// Sandboxes lists the sandboxes of every engine.
func (a *Core) Sandboxes() ([]Sandbox, error) {
	rows, err := a.rows()
	if err != nil {
		return nil, err
	}
	out := make([]Sandbox, len(rows))
	for i, r := range rows {
		st, status := friendlyState(r.Status)
		key := r.KeyKind
		if key == "home" {
			key = r.Key
		}
		out[i] = Sandbox{
			Name: r.Name, State: st, Status: status, Created: friendlyFix(r.Created),
			Engine: r.Engine, RunsIn: a.runsIn(r),
			KeyKind: r.KeyKind, Key: r.Key, KeyName: key[strings.LastIndexByte(key, '/')+1:],
			KeyInside: r.KeyMode == "file", Providers: r.Providers, Gateway: r.Gateway != "", APIKeysInside: a.keysInside(r), NoNetwork: r.Network == "none", Filtered: r.Network == "filtered",
			Limits: limitsOf(r), Source: r.Source, Version: r.Version,
		}
	}
	return out, nil
}

// runsIn says briefly what isolates a sandbox: "docker", "gvisor" or "kata"
// for those Docker runtimes, or "apple" for a VM of its own.
func (a *Core) runsIn(r backend.Row) string {
	if r.Engine == "apple" {
		return "apple"
	}
	if k := platform.Kind(a.dockerInfo, r.Runtime); k != "" {
		return k
	}
	return "docker"
}

// Names lists every sandbox's name.
func (a *Core) Names() ([]string, error) {
	rows, err := a.rows()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names, nil
}

func noSuch(name string) error {
	return fmt.Errorf("no sandbox named '%s' (see: opencode-sandbox ls)", name)
}

// Removed is what Remove deleted.
type Removed struct {
	Name string
	// KeyFingerprint is the disposable key deleted with it; "" if none.
	KeyFingerprint string
	Disposable     bool
}

// Remove deletes a sandbox, its files and its disposable key.
func (a *Core) Remove(n string) (Removed, error) {
	c := state.Prefix + n
	e := a.engineOf(n)
	if e == nil {
		return Removed{}, noSuch(n)
	}
	kind, err := e.Label(c, state.Label("keykind"))
	if err != nil {
		return Removed{}, err
	}
	key, err := e.Label(c, state.Label("key"))
	if err != nil {
		return Removed{}, err
	}
	if err := e.Remove(c); err != nil {
		return Removed{}, err
	}
	sshagent.Stop(state.AgentDir(n))
	_ = os.Remove(state.ConfigFile(n))
	_ = os.RemoveAll(state.SandboxDir(n))
	_ = os.RemoveAll(state.GatewayDir(n)) // its gateway ended with it
	_ = os.RemoveAll(state.NetDir(n))     // and its rules are no other sandbox's
	r := Removed{Name: n}
	if kind == "disposable" {
		r.Disposable = true
		r.KeyFingerprint = a.quietFingerprint(key + ".pub")
		a.discardKey(n, key)
	}
	return r, nil
}

// CopyIn copies files or folders from this machine into a sandbox's
// /workspace, open or closed. It checks every path before copying any.
func (a *Core) CopyIn(name string, paths []string, done func(src string)) error {
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return noSuch(name)
	}
	for _, src := range paths {
		if !present(src) {
			return fmt.Errorf("no such file or folder: %s", src)
		}
		real, err := state.ReadlinkF(src)
		if err != nil {
			return fmt.Errorf("bad path: %s", src)
		}
		if err := a.checkPath(real, "copy"); err != nil {
			return err
		}
	}
	for _, src := range paths {
		if w := a.DotFiles(src).Warning(src); w != "" {
			a.Rep.Warn(w)
		}
	}
	undo, err := a.mountable(e, name)
	if err != nil {
		return err
	}
	defer undo()
	for _, src := range paths {
		if err := e.CopyIn(c, src, "/workspace/", false); err != nil {
			return fmt.Errorf("could not copy %s into '%s'", src, name)
		}
		if done != nil {
			done(src)
		}
	}
	return nil
}

// CopyOut copies files or folders out of a sandbox (paths relative to its
// /workspace) into the folder here, never overwriting. done gets each
// sandbox path and where it went.
func (a *Core) CopyOut(name string, paths []string, here string, done func(src, dst string)) error {
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return noSuch(name)
	}
	here, err := filepath.EvalSymlinks(here)
	if err != nil {
		return err
	}
	if err := a.checkPath(here, "copy files into"); err != nil {
		return err
	}
	source := func(p string) (string, string) {
		src := p
		if !strings.HasPrefix(p, "/") {
			src = "/workspace/" + p
		}
		base := strings.TrimSuffix(src, "/")
		return src, base[strings.LastIndexByte(base, '/')+1:]
	}
	for _, p := range paths {
		_, base := source(p)
		if base == "" || base == "." || base == ".." {
			return fmt.Errorf("name a file or folder in the sandbox, not %s", p)
		}
		if present(here + "/" + base) {
			return fmt.Errorf("%s/%s already exists; move it away or copy into another folder", here, base)
		}
	}
	undo, err := a.mountable(e, name)
	if err != nil {
		return err
	}
	defer undo()
	for _, p := range paths {
		src, base := source(p)
		if err := e.CopyOut(c, src, here+"/"); err != nil {
			return fmt.Errorf("could not copy %s out of '%s'", src, name)
		}
		if done != nil {
			done(src, here+"/"+base)
		}
	}
	return nil
}

// mountable gets a closed sandbox ready for copying files in or out where
// the engine mounts its binds for that (docker): everything it mounts must
// exist, but its config is only written when it starts (and is gone after a
// reboot), and its agent's socket folder is removed when it closes. The
// placeholders it makes are removed by the function it returns.
func (a *Core) mountable(e backend.Engine, name string) (func(), error) {
	c := state.Prefix + name
	if !e.Caps().CopyNeedsBinds || e.State(c) == "running" {
		return func() {}, nil
	}
	if err := a.checkBinds(e, name); err != nil {
		return nil, err
	}
	srcs, err := e.BindSources(c)
	if err != nil {
		return nil, err
	}
	var made []string
	for _, s := range srcs {
		s = strings.TrimPrefix(s, "/host_mnt")
		sock := strings.HasSuffix(s, "/agents/"+name+"/sock")
		if present(s) || !sock && !strings.HasSuffix(s, "/configs/"+name+".json") {
			continue // checkBinds explained anything else that's missing
		}
		if err := state.EnsureRunDir(); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(s), 0o700); err != nil {
			return nil, err
		}
		if sock {
			err = os.Mkdir(s, 0o700)
		} else {
			err = os.WriteFile(s, nil, 0o600)
		}
		if err != nil {
			return nil, err
		}
		made = append(made, s)
	}
	return func() {
		for _, s := range made {
			if fi, err := os.Lstat(s); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 && e.State(c) != "running" {
				_ = os.Remove(s)
			}
		}
		a.agents.Close(name)
	}, nil
}

// checkBinds explains a file a sandbox mounts that has gone, e.g. the
// known_hosts or a key of a bash launcher's clone that was deleted, instead
// of letting the engine fail with a terse error.
func (a *Core) checkBinds(e backend.Engine, name string) error {
	srcs, err := e.BindSources(state.Prefix + name)
	if err != nil {
		return err
	}
	runDirs := []string{state.RunDir()}
	if real, err := state.ReadlinkF(state.RunDir()); err == nil {
		runDirs = append(runDirs, real)
	}
	for _, s := range srcs {
		// Docker Desktop reports host paths as its VM sees them.
		s = strings.TrimPrefix(s, "/host_mnt")
		regenerated := false // the config and agent socket are rewritten on every start
		for _, d := range runDirs {
			regenerated = regenerated || strings.HasPrefix(s, d+"/")
		}
		if regenerated {
			continue
		}
		if !present(s) {
			return fmt.Errorf("sandbox '%s' needs %s, which no longer exists (was it in the folder of an older opencode-sandbox that has been moved or deleted?)", name, s)
		}
	}
	return nil
}

// PubKey is the public key a sandbox uses.
func (a *Core) PubKey(name string) (string, error) {
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return "", noSuch(name)
	}
	kind, err := e.Label(c, state.Label("keykind"))
	if err != nil {
		return "", err
	}
	if kind == "none" {
		return "", fmt.Errorf("sandbox '%s' has no SSH key", name)
	}
	key, err := e.Label(c, state.Label("key"))
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", fmt.Errorf("can't read %s.pub: %v", key, errors.Unwrap(err))
	}
	return string(data), nil
}

// Fingerprint is the SHA256 fingerprint of a sandbox's key; "" if it has
// none or it can't be read.
func (a *Core) Fingerprint(s Sandbox) string {
	if s.KeyKind == "none" || s.Key == "" {
		return ""
	}
	return a.quietFingerprint(s.Key + ".pub")
}

func (a *Core) quietFingerprint(pub string) string {
	var out bytes.Buffer
	if code, err := a.r.Run(&run.Cmd{Args: []string{"ssh-keygen", "-lf", pub}, Stdout: &out}); err != nil || code != 0 {
		return ""
	}
	if w := strings.Fields(out.String()); len(w) > 1 {
		return w[1]
	}
	return ""
}

// Agent is the ssh-agent of an open sandbox.
type Agent struct {
	Sandbox string
	Key     string   // the key it was started for
	Loaded  []string // ssh-add -l's lines
}

// Agents lists the ssh-agents of open sandboxes.
func (a *Core) Agents() []Agent {
	a.agents.GC()
	var out []Agent
	for _, d := range sshagent.AgentDirs() {
		sock := sshagent.Sock(d)
		if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		key, _ := os.ReadFile(d + "/key")
		var b bytes.Buffer
		_, _ = a.r.Run(&run.Cmd{Args: []string{"ssh-add", "-l"}, Env: []string{"SSH_AUTH_SOCK=" + sock}, Stdout: &b, Stderr: &b})
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		out = append(out, Agent{Sandbox: filepath.Base(d), Key: strings.TrimRight(string(key), "\n"), Loaded: lines})
	}
	return out
}

// AgentRunning says whether a sandbox's ssh-agent is running.
func (a *Core) AgentRunning(name string) bool {
	fi, err := os.Stat(sshagent.Sock(state.AgentDir(name)))
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// StopAgents stops all sandbox ssh-agents (cutting SSH for open sandboxes).
func (a *Core) StopAgents() {
	for _, d := range sshagent.AgentDirs() {
		sshagent.Stop(d)
	}
	// the single shared agent used by early versions of the bash launcher
	sshagent.KillPidFile(state.RunDir() + "/ssh-agent.pid")
	_ = os.RemoveAll(state.RunDir() + "/sock")
	_ = os.Remove(state.RunDir() + "/ssh-agent.pid")
}
