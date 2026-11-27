package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
	"github.com/apocas/opencode-sandbox/internal/tty"
)

// --- --list and --resume ---------------------------------------------------

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

// friendlyState turns Docker's status into open/closed/new.
func friendlyState(s string) string {
	switch {
	case upRe.MatchString(s):
		s = upRe.ReplaceAllString(s, "open ($1)")
	case exitedRe.MatchString(s):
		s = exitedRe.ReplaceAllString(s, "closed ")
	case s == "Created":
		s = "new"
	case s == "Exited":
		s = "closed"
	case s == "Up":
		s = "open"
	}
	return friendlyFix(s)
}

// list prints the sandboxes; numbered for picking one.
func (a *app) list(w io.Writer, numbered bool) error {
	rows, err := a.rows()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no sandboxes")
		return nil
	}
	row := func(num, name, key, prov, st, created, runs, src string) {
		fmt.Fprintf(w, "%s%-22s %-13s %-20s %-22s %-15s %-7s %s\n", num, name, key, prov, st, created, runs, src)
	}
	num := ""
	if numbered {
		num = "#   "
	}
	row(num, "NAME", "SSH KEY", "PROVIDER", "STATE", "CREATED", "RUNS IN", "COPIED FROM")
	inside := false
	for i, r := range rows {
		key := r.KeyKind
		if key == "home" {
			key = r.Key
		}
		key = key[strings.LastIndexByte(key, '/')+1:]
		if r.KeyMode == "file" {
			key += "*"
			inside = true
		}
		prov := r.Providers
		if prov == "" {
			prov = "all (old sandbox)"
		}
		if numbered {
			num = fmt.Sprintf("%-3d ", i+1)
		}
		row(num, r.Name, key, prov, friendlyState(r.Status), friendlyFix(r.Created), a.runsIn(r), r.Source)
	}
	if inside {
		fmt.Fprintln(w, "* private key stored inside the sandbox (no host ssh-agent on this setup)")
	}
	return nil
}

// runsIn says briefly what isolates a sandbox: "docker", "gvisor" or "kata"
// for those Docker runtimes, or "apple" for a VM of its own.
func (a *app) runsIn(r backend.Row) string {
	if r.Engine == "apple" {
		return "apple"
	}
	if k := platform.Kind(a.dockerInfo, r.Runtime); k != "" {
		return k
	}
	return "docker"
}

func (a *app) pick() (string, error) {
	rows, err := a.rows()
	if err != nil {
		return "", err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	if len(names) == 0 {
		return "", errors.New("no sandboxes yet; create one with: opencode-sandbox -w DIR")
	}
	if err := a.list(os.Stderr, true); err != nil {
		return "", err
	}
	line, ok := tty.ReadLine(fmt.Sprintf("Resume which sandbox? [1-%d] ", len(names)))
	if !ok {
		return "", errors.New("no choice made")
	}
	n, ok := choice(line, len(names))
	if !ok {
		return "", fmt.Errorf("no such choice: %s", line)
	}
	return names[n-1], nil
}

func (a *app) resume(name string) (int, error) {
	if name == "" {
		var err error
		if name, err = a.pick(); err != nil {
			return 0, err
		}
	}
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return 0, fmt.Errorf("no sandbox named '%s' (see --list)", name)
	}
	if err := a.prepare(e); err != nil {
		return 0, err
	}
	label := func(k string) (string, error) { return e.Label(c, state.Label(k)) }
	mode, err := label("keymode")
	if err != nil {
		return 0, err
	}
	if mode == "" { // created before 0.2.0
		kind, err := label("keykind")
		if err != nil {
			return 0, err
		}
		mode = state.KeyMode(kind, "")
	}
	if mode == "agent" {
		rt, err := label("runtime")
		if err != nil {
			return 0, err
		}
		if p := platform.Decide(a.dockerInfo, rt); e.Name() != "docker" || !p.AgentOK {
			return 0, fmt.Errorf("sandbox '%s' uses an ssh-agent on this machine, which isn't possible here: %s", name, p.NoAgentWhy)
		}
		key, err := label("key")
		if err != nil {
			return 0, err
		}
		if err := a.agents.Ensure(name, key); err != nil {
			return 0, err
		}
	}
	if e.State(c) != "running" {
		provs, err := label("providers")
		if err != nil {
			return 0, err
		}
		if provs != "" {
			if err := a.writeConfig(e, name, provs); err != nil {
				return 0, err
			}
		} else {
			a.note("sandbox '%s' predates provider selection and sees all your providers; recreate it to limit them", name)
		}
		if err := a.checkBinds(e, name); err != nil {
			return 0, err
		}
	}
	v, err := label("version")
	if err != nil {
		return 0, err
	}
	if v != Version {
		if v == "" {
			v = "before 0.1.0"
		}
		a.note("sandbox '%s' was created by opencode-sandbox %s (you have %s) and keeps its original image and settings; create a new sandbox to get the update", name, v, Version)
	}
	return a.open(e, name)
}

// checkBinds explains a file a sandbox mounts that has gone, e.g. the
// known_hosts or a key of a bash launcher's clone that was deleted, instead
// of letting Docker fail with a terse error.
func (a *app) checkBinds(e backend.Engine, name string) error {
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

// open starts (or attaches to) a sandbox on this terminal and returns the
// engine client's exit status. Closing the terminal stops the sandbox; only
// ctrl-] ctrl-] leaves it running, where the engine can detach.
func (a *app) open(e backend.Engine, name string) (int, error) {
	c := state.Prefix + name
	var argv []string
	switch {
	case e.State(c) != "running":
		a.note("opening sandbox '%s'", name)
		argv = e.StartArgv(c, state.DetachKeys)
	case e.Caps().Attach:
		a.note("sandbox '%s' is already open elsewhere; attaching to it (detach: ctrl-] ctrl-])", name)
		argv = e.AttachArgv(c, state.DetachKeys)
	default:
		return 0, fmt.Errorf("sandbox '%s' is already open in another terminal, and Apple container can't attach a second one; close it there first, or get a shell in it with: opencode-sandbox --shell %s", name, name)
	}
	if o, ok := a.r.(run.OS); ok && o.Debug != nil {
		fmt.Fprintf(o.Debug, "+ %s\n", run.Quote(argv))
	}
	// Caught before the client starts, so none is missed. SIGINT is the
	// client's to handle (it forwards it to opencode); the launcher only
	// mustn't die of it.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			rc, _ := run.Status(err)
			a.agents.Close(name) // closed rather than detached? then its key leaves memory now
			return rc, nil
		case s := <-sigs:
			if s == syscall.SIGINT {
				continue
			}
			_ = cmd.Process.Kill()
			// Detached, so it completes even if this launcher is killed next.
			if self, err := os.Executable(); err == nil {
				cleanup := exec.Command(self, hangupCleanup, name)
				cleanup.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				_ = cleanup.Start()
			}
			return 129, nil
		}
	}
}

// hangupCleanup runs in its own session after the terminal of an open
// sandbox went away: it stops the sandbox and its agent.
func (a *app) hangupCleanup(name string) {
	if !validName.MatchString(name) {
		return
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	a.findEngines()
	if e := a.engineOf(name); e != nil {
		_ = e.Stop(state.Prefix+name, 5)
	}
	sshagent.Stop(state.AgentDir(name))
}

// --- --rm --------------------------------------------------------------------

func (a *app) remove(args []string) error {
	if len(args) == 0 {
		return errors.New("--rm needs sandbox names, or --all")
	}
	targets := args
	if args[0] == "--all" {
		rows, err := a.rows()
		if err != nil {
			return err
		}
		var names []string
		for _, r := range rows {
			names = append(names, r.Name)
		}
		if len(names) == 0 {
			fmt.Println("no sandboxes")
			return nil
		}
		reply, ok := tty.ReadLine(fmt.Sprintf("Delete all %d sandboxes, their files and disposable keys? [y/N] ", len(names)))
		if !ok || !strings.HasPrefix(reply, "y") && !strings.HasPrefix(reply, "Y") {
			fmt.Println("nothing deleted")
			return nil
		}
		targets = names
	}
	for _, n := range targets {
		c := state.Prefix + n
		e := a.engineOf(n)
		if e == nil {
			a.note("no sandbox named '%s'", n)
			continue
		}
		kind, err := e.Label(c, state.Label("keykind"))
		if err != nil {
			return err
		}
		key, err := e.Label(c, state.Label("key"))
		if err != nil {
			return err
		}
		if err := e.Remove(c); err != nil {
			return err
		}
		sshagent.Stop(state.AgentDir(n))
		_ = os.RemoveAll(state.SandboxDir(n))
		_ = os.Remove(state.ConfigFile(n))
		if kind == "disposable" {
			fp := a.quietFingerprint(key + ".pub")
			a.discardKey(n, key)
			fmt.Printf("removed %s and its disposable key %s (remove it from any authorized_keys you added it to)\n", n, fp)
		} else {
			fmt.Printf("removed %s\n", n)
		}
	}
	return nil
}

func (a *app) quietFingerprint(pub string) string {
	var out bytes.Buffer
	if code, err := a.r.Run(&run.Cmd{Args: []string{"ssh-keygen", "-lf", pub}, Stdout: &out}); err != nil || code != 0 {
		return ""
	}
	if w := strings.Fields(out.String()); len(w) > 1 {
		return w[1]
	}
	return ""
}

// --- --cp and --get ----------------------------------------------------------

// copyIn copies files and folders from this machine into a sandbox's
// /workspace. Works open or closed; opencode sees new files at once.
func (a *app) copyIn(args []string) error {
	if len(args) < 2 || args[0] == "" {
		return errors.New("usage: opencode-sandbox --cp NAME PATH...")
	}
	name, paths := args[0], args[1:]
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return fmt.Errorf("no sandbox named '%s' (see --list)", name)
	}
	for _, src := range paths { // check everything before copying anything
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
	done, err := a.mountable(e, name)
	if err != nil {
		return err
	}
	defer done()
	for _, src := range paths {
		if err := e.CopyIn(c, src, "/workspace/", false); err != nil {
			return fmt.Errorf("could not copy %s into '%s'", src, name)
		}
		fmt.Printf("copied %s into %s:/workspace/\n", src, name)
	}
	return nil
}

// copyOut copies files and folders out of a sandbox into the current
// folder. It never overwrites; what comes out was written by the agent, so
// treat it as untrusted.
func (a *app) copyOut(args []string) error {
	if len(args) < 2 || args[0] == "" {
		return errors.New("usage: opencode-sandbox --get NAME PATH...")
	}
	name, paths := args[0], args[1:]
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return fmt.Errorf("no sandbox named '%s' (see --list)", name)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	here, err := filepath.EvalSymlinks(wd)
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
			return fmt.Errorf("%s/%s already exists; move it away or run --get from another folder", here, base)
		}
	}
	done, err := a.mountable(e, name)
	if err != nil {
		return err
	}
	defer done()
	for _, p := range paths {
		src, base := source(p)
		if err := e.CopyOut(c, src, here+"/"); err != nil {
			return fmt.Errorf("could not copy %s out of '%s'", src, name)
		}
		fmt.Printf("copied %s:%s to %s/%s\n", name, src, here, base)
	}
	return nil
}

// mountable gets a closed sandbox ready for copying files in or out where
// the engine mounts its binds for that (docker): everything it mounts must
// exist, but its config is only written when it starts (and is gone after a
// reboot), and its agent's socket folder is removed when it closes. The
// placeholders it makes are removed by the function it returns.
func (a *app) mountable(e backend.Engine, name string) (func(), error) {
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

// --- agents, keys and shells -------------------------------------------------

func (a *app) status() {
	a.agents.GC()
	found := false
	for _, d := range sshagent.AgentDirs() {
		sock := sshagent.Sock(d)
		if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		found = true
		key, _ := os.ReadFile(d + "/key")
		fmt.Printf("sandbox %s (%s):\n", filepath.Base(d), strings.TrimRight(string(key), "\n"))
		var out bytes.Buffer
		_, _ = a.r.Run(&run.Cmd{Args: []string{"ssh-add", "-l"}, Env: []string{"SSH_AUTH_SOCK=" + sock}, Stdout: &out, Stderr: &out})
		for _, line := range strings.SplitAfter(out.String(), "\n") {
			if line != "" {
				fmt.Print("  " + line)
			}
		}
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			fmt.Println()
		}
	}
	if !found {
		fmt.Println("no sandbox agents running (they only run while a sandbox is open)")
	}
}

func (a *app) stopAgents() {
	for _, d := range sshagent.AgentDirs() {
		sshagent.Stop(d)
	}
	// the single shared agent used by early versions of the bash launcher
	sshagent.KillPidFile(state.RunDir() + "/ssh-agent.pid")
	_ = os.RemoveAll(state.RunDir() + "/sock")
	_ = os.Remove(state.RunDir() + "/ssh-agent.pid")
	fmt.Println("all sandbox ssh-agents stopped")
}

func (a *app) pubkey(name string) error {
	if name == "" {
		return errors.New("--pubkey needs a sandbox name (see --list)")
	}
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return fmt.Errorf("no sandbox named '%s' (see --list)", name)
	}
	kind, err := e.Label(c, state.Label("keykind"))
	if err != nil {
		return err
	}
	if kind == "none" {
		return fmt.Errorf("sandbox '%s' has no SSH key", name)
	}
	key, err := e.Label(c, state.Label("key"))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(key + ".pub")
	if err != nil {
		return fmt.Errorf("can't read %s.pub: %v", key, errors.Unwrap(err))
	}
	_, err = os.Stdout.Write(data)
	return err
}

func (a *app) shell(name string) error {
	if name == "" {
		return errors.New("--shell needs a sandbox name (see --list)")
	}
	e := a.engineOf(name)
	if e == nil || e.State(state.Prefix+name) != "running" {
		return fmt.Errorf("sandbox '%s' isn't open; open it first with: opencode-sandbox --resume %s", name, name)
	}
	argv := e.ExecArgv(state.Prefix+name, a.tty, state.DetachKeys, "bash")
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	if o, ok := a.r.(run.OS); ok && o.Debug != nil {
		fmt.Fprintf(o.Debug, "+ %s\n", run.Quote(argv))
	}
	return syscall.Exec(path, argv, os.Environ())
}
