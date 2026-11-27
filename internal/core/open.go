package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// ErrHangup: the terminal went away while a sandbox was open on it. The
// sandbox is being stopped; exit with status 129.
var ErrHangup = errors.New("the terminal went away")

// Prepare gets a sandbox ready to open: its image, its ssh-agent, a fresh
// copy of its config (what changed in yours is picked up) and the files it
// mounts. Opening one that's already open (attaching) needs it too.
func (a *Core) Prepare(name string) error {
	c := state.Prefix + name
	e := a.engineOf(name)
	if e == nil {
		return noSuch(name)
	}
	if err := a.prepare(e); err != nil {
		return err
	}
	label := func(k string) (string, error) { return e.Label(c, state.Label(k)) }
	mode, err := label("keymode")
	if err != nil {
		return err
	}
	if mode == "" { // created before 0.2.0
		kind, err := label("keykind")
		if err != nil {
			return err
		}
		mode = state.KeyMode(kind, "")
	}
	if mode == "agent" {
		rt, err := label("runtime")
		if err != nil {
			return err
		}
		if p := platform.Decide(a.dockerInfo, rt); e.Name() != "docker" || !p.AgentOK {
			return fmt.Errorf("sandbox '%s' uses an ssh-agent on this machine, which isn't possible here: %s", name, p.NoAgentWhy)
		}
		key, err := label("key")
		if err != nil {
			return err
		}
		if err := a.agents.Ensure(name, key); err != nil {
			return err
		}
	}
	if e.State(c) != "running" {
		provs, err := label("providers")
		if err != nil {
			return err
		}
		gw, err := label("gateway")
		if err != nil {
			return err
		}
		network, err := label("network")
		if err != nil {
			return err
		}
		if provs != "" {
			if err := a.writeConfig(e, name, provs, gw != "", network == "none"); err != nil {
				return err
			}
		} else {
			a.note("sandbox '%s' predates provider selection and sees all your providers; recreate it to limit them", name)
		}
		if err := a.checkBinds(e, name); err != nil {
			return err
		}
	}
	v, err := label("version")
	if err != nil {
		return err
	}
	if v != Version {
		if v == "" {
			v = "before 0.1.0"
		}
		a.note("sandbox '%s' was created by opencode-sandbox %s (you have %s) and keeps its original image and settings; create a new sandbox to get the update", name, v, Version)
	}
	return nil
}

// Open opens a prepared sandbox on this terminal and returns the engine
// client's exit status. Closing the terminal stops the sandbox (ErrHangup);
// only ctrl-] ctrl-] leaves it running, where the engine can detach.
func (a *Core) Open(name string) (int, error) {
	o := a.OpenCommand(name)
	o.SetStdin(os.Stdin)
	o.SetStdout(os.Stdout)
	o.SetStderr(os.Stderr)
	err := o.Run()
	return o.Code, err
}

// OpenCommand opens a prepared sandbox when run, on the terminal it's given;
// a dashboard can hand its terminal over to it (it's a tea.ExecCommand).
type OpenCommand struct {
	a              *Core
	name           string
	stdin          io.Reader
	stdout, stderr io.Writer
	// Code is the engine client's exit status, once Run returns.
	Code int
	// Detached: the sandbox was left running (ctrl-] ctrl-]).
	Detached bool
}

// OpenCommand makes an OpenCommand for sandbox name.
func (a *Core) OpenCommand(name string) *OpenCommand {
	return &OpenCommand{a: a, name: name, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
}

func (o *OpenCommand) SetStdin(r io.Reader)  { o.stdin = r }
func (o *OpenCommand) SetStdout(w io.Writer) { o.stdout = w }
func (o *OpenCommand) SetStderr(w io.Writer) { o.stderr = w }

// Run opens the sandbox and waits until it's closed or detached.
func (o *OpenCommand) Run() error {
	a, name, c := o.a, o.name, state.Prefix+o.name
	e := a.engineOf(name)
	if e == nil {
		return noSuch(name)
	}
	var argv []string
	switch {
	case e.State(c) != "running":
		a.note("opening sandbox '%s'", name)
		argv = e.StartArgv(c, state.DetachKeys)
	case e.Caps().Attach:
		a.note("sandbox '%s' is already open elsewhere; attaching to it (detach: ctrl-] ctrl-])", name)
		argv = e.AttachArgv(c, state.DetachKeys)
	default:
		return fmt.Errorf("sandbox '%s' is already open in another terminal, and Apple container can't attach a second one; close it there first, or get a shell in it with: opencode-sandbox shell %s", name, name)
	}
	if r, ok := a.r.(run.OS); ok && r.Debug != nil {
		fmt.Fprintf(r.Debug, "+ %s\n", run.Quote(argv))
	}
	// Its providers' API keys stay here: the gateway that puts them into its
	// requests starts with it, and ends when it closes.
	if gw, _ := e.Label(c, state.Label("gateway")); gw != "" {
		if err := a.startGateway(name); err != nil {
			a.Rep.Warn(fmt.Sprintf("can't start the gateway of sandbox '%s', so opencode won't reach its providers: %v", name, err))
		}
	}
	// Caught before the client starts, so none is missed. SIGINT is the
	// client's to handle (it forwards it to opencode); the launcher only
	// mustn't die of it.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.stdin, o.stdout, o.stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			o.Code, _ = run.Status(err)
			o.Detached = e.State(c) == "running"
			a.agents.Close(name) // closed rather than detached? then its key leaves memory now
			return nil
		case s := <-sigs:
			if s == syscall.SIGINT {
				continue
			}
			_ = cmd.Process.Kill()
			// Detached, so it completes even if this launcher is killed next.
			if self, err := os.Executable(); err == nil {
				cleanup := exec.Command(self, HangupCleanupArg, name)
				cleanup.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				_ = cleanup.Start()
			}
			o.Code = 129
			return ErrHangup
		}
	}
}

// HangupCleanup runs in its own session after the terminal of an open
// sandbox went away: it stops the sandbox and its agent.
func (a *Core) HangupCleanup(name string) {
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

// ShellArgv is the command that runs bash inside an open sandbox.
func (a *Core) ShellArgv(name string) ([]string, error) {
	e := a.engineOf(name)
	if e == nil {
		return nil, noSuch(name)
	}
	if e.State(state.Prefix+name) != "running" {
		return nil, fmt.Errorf("sandbox '%s' isn't open; open it first with: opencode-sandbox open %s", name, name)
	}
	return e.ExecArgv(state.Prefix+name, a.TTY, state.DetachKeys, "bash"), nil
}
