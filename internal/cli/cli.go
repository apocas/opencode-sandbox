// Package cli is opencode-sandbox's command line: subcommands for scripts and
// quick use, styled on a terminal and plain elsewhere. Without a command, it
// opens the dashboard (package tui).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"charm.land/lipgloss/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/selfupdate"
	"github.com/apocas/opencode-sandbox/internal/tui"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// usageError is a mistake on the command line (exit status 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// renamed maps the options of 0.5 and earlier to the commands that replace
// them.
var renamed = map[string]string{
	"--list": "ls", "--resume": "open", "--pubkey": "key", "--shell": "shell", "--cp": "cp", "--get": "get",
	"--rm": "rm", "--status": "agents", "--stop-agent": "agents stop", "--build": "build", "--health": "health",
	"-w": "new DIR", "-n": "new -n NAME", "-k": "new -k KEY", "-p": "new -p PROVIDER", "--": "new -- ARGS",
}

type cli struct {
	t   *ui.Theme
	c   *core.Core
	rep *ui.Reporter
	// stdinTTY: pickers can ask; outTTY: output is styled for a terminal.
	stdinTTY, outTTY bool
}

// Main runs the command line and returns the exit status.
func Main(args []string) int {
	t := ui.DetectTheme()
	x := &cli{t: t, c: core.New(), stdinTTY: ui.IsTerminal(os.Stdin), outTTY: ui.IsTerminal(os.Stdout)}
	x.rep = ui.NewReporter(t)
	x.c.Rep = x.rep
	x.c.TTY = x.stdinTTY && x.outTTY

	code, err := x.main(args)
	var ee *run.ExitError
	var ue usageError
	switch {
	case err == nil:
		return code
	case errors.As(err, &ee):
		return ee.Code // the program that failed already said why
	case errors.Is(err, ui.ErrCancelled):
		x.rep.Note("cancelled")
		return 1
	case errors.As(err, &ue):
		x.fail(err)
		return 2
	default:
		x.fail(err)
		return 1
	}
}

func (x *cli) fail(err error) {
	x.rep.Fail()
	if ui.IsTerminal(os.Stderr) {
		lipgloss.Fprintln(os.Stderr, x.t.Failure(err.Error()))
	} else {
		fmt.Fprintf(os.Stderr, "opencode-sandbox: %v\n", err)
	}
}

func (x *cli) main(args []string) (int, error) {
	cmd, rest := "", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	switch cmd {
	case "":
		if !x.stdinTTY || !x.outTTY {
			x.help(os.Stderr, "")
			return 2, nil
		}
		if os.Getuid() == 0 {
			return 0, errors.New(platform.RootRefusal)
		}
		// Without an engine, the dashboard shows what to install.
		return tui.Run(x.c, x.t, x.c.Setup()), nil
	case "help", "-h", "--help":
		x.help(os.Stdout, first(rest))
		return 0, nil
	case "version", "-V", "--version":
		fmt.Printf("opencode-sandbox %s (opencode %s)\n", core.Version, core.OpencodeVersion())
		return 0, nil
	case core.HangupCleanupArg:
		x.c.HangupCleanup(first(rest))
		return 0, nil
	case core.GatewayArg:
		x.c.Gateway(first(rest))
		return 0, nil
	case "health":
		return x.health()
	case "update": // needs no engine, and may run as root where the program is root's
		return x.update(rest)
	case "setup": // what it sets up may be the engine
		return x.setup(rest)
	}
	if to, ok := renamed[cmd]; ok {
		return 0, usagef("%s is now: opencode-sandbox %s (see: opencode-sandbox help)", cmd, to)
	}
	if wantsHelp(rest) {
		x.help(os.Stdout, cmd)
		return 0, nil
	}
	run, ok := map[string]func([]string) (int, error){
		"new": x.newCmd, "ls": x.ls, "open": x.open, "shell": x.shell, "key": x.key,
		"cp": x.cp, "get": x.get, "rm": x.rm, "build": x.build, "agents": x.agents,
	}[cmd]
	if !ok {
		return 0, usagef("unknown command: %s (see: opencode-sandbox help)", cmd)
	}
	if err := x.c.Setup(); err != nil {
		return 0, err
	}
	return run(rest)
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// out prints a line to stdout, styled where it's a terminal.
func (x *cli) out(s string) { lipgloss.Fprintln(os.Stdout, s) }

// done says something worked: ✓ on a terminal, plain otherwise.
func (x *cli) done(msg string) {
	if x.outTTY {
		x.out(x.t.Success(msg))
	} else {
		fmt.Println(msg)
	}
}

// --- commands ----------------------------------------------------------------

func (x *cli) newCmd(args []string) (int, error) {
	var o core.CreateOptions
	var prov string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func(what string) (string, error) {
			if i+1 >= len(args) {
				return "", usagef("%s needs %s", a, what)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case a == "-n":
			o.Name, err = value("a name")
		case a == "-k":
			o.Key, err = value("a key name, 'new' or 'none'")
		case a == "-p":
			prov, err = value("a provider name (or several: a,b)")
		case a == "--empty":
			o.Empty = true
		case a == "--network":
			o.Network, err = value("open or none")
		case a == "--engine":
			o.Engine, err = value("docker or apple")
		case a == "--runtime":
			o.Runtime, err = value("the name of a Docker runtime")
		case a == "--gateway":
			o.Gateway, err = value("on or off")
		case a == "--":
			o.Args = args[i+1:]
			i = len(args)
		case a == "-w":
			return 0, usagef("-w is gone: name the folder instead, as in: opencode-sandbox new DIR")
		case strings.HasPrefix(a, "-"):
			return 0, usagef("unknown option for new: %s (see: opencode-sandbox help new)", a)
		case o.Workspace != "":
			return 0, usagef("new takes one folder (got %s and %s); pass opencode's own arguments after --", o.Workspace, a)
		default:
			o.Workspace = a
		}
		if err != nil {
			return 0, err
		}
	}
	if err := x.c.CheckPlace(o.Engine, o.Runtime); err != nil {
		return 0, err
	}
	network, err := core.NetworkFor(o.Network)
	if err != nil {
		return 0, usageError{err.Error()}
	}
	if network == "none" && o.Key == "" {
		o.Key = "none" // nowhere to log in to: no question to ask
	}
	ws := "" // of a sandbox that starts empty
	switch {
	case o.Empty && o.Workspace != "":
		return 0, usagef("new takes a folder to copy (%s) or --empty, not both", o.Workspace)
	case !o.Empty:
		if ws, err = x.c.ResolveWorkspace(o.Workspace); err != nil {
			return 0, err
		}
		o.Workspace = ws
	}
	if o.Name == "" {
		o.Name = x.c.DefaultName(ws)
	}
	if err := x.c.CheckName(o.Name); err != nil {
		return 0, err
	}
	if o.Key == "" {
		if !x.stdinTTY {
			return 0, usagef("no terminal to ask which SSH key to use; pass -k NAME, -k new or -k none")
		}
		choices, why := x.c.KeyChoicesFor(o.Engine, o.Runtime)
		var opts []ui.Option
		for _, k := range choices {
			opts = append(opts, ui.Option{Value: k.Value, Label: k.Label, Detail: k.Detail})
		}
		if o.Key, err = x.t.Pick(fmt.Sprintf("Which SSH key may sandbox '%s' use?", o.Name), why, opts); err != nil {
			return 0, err
		}
	}
	if prov != "" {
		o.Providers = strings.Split(prov, ",")
	} else if x.stdinTTY {
		ps, err := x.c.ProviderChoices()
		if err != nil {
			return 0, err
		}
		if len(ps) > 1 {
			var opts []ui.Option
			for _, p := range ps {
				opts = append(opts, ui.Option{Value: p.ID, Label: p.ID, Detail: p.Description})
			}
			if o.Providers, err = x.t.PickMany(fmt.Sprintf("Which opencode providers may sandbox '%s' use?", o.Name),
				"Space selects, enter confirms. The sandbox gets only these providers.", opts); err != nil {
				return 0, err
			}
		}
	}
	// What often holds secrets is copied like the rest: say so first, while
	// there's nothing to undo.
	if w := x.c.DotFiles(ws).Warning(ui.Tilde(ws)); w != "" {
		x.rep.Warn(w)
		if x.stdinTTY && ui.IsTerminal(os.Stderr) {
			lipgloss.Fprint(os.Stderr, x.t.Dim.Render("Press enter to copy it as it is, or ctrl-c to stop… "))
			waitEnter()
		}
	}
	created, err := x.c.Create(o)
	if err != nil {
		return 0, err
	}
	if ws == "" {
		x.done(fmt.Sprintf("created sandbox '%s', with nothing copied into it", created.Name))
	} else {
		x.done(fmt.Sprintf("created sandbox '%s' from %s", created.Name, ui.Tilde(ws)))
	}
	if created.NewKey != nil {
		x.showKey(created.NewKey)
	}
	return x.openNow(created.Name)
}

// showKey shows a new disposable key, to authorize where the sandbox may log
// in, and waits for enter on a terminal.
func (x *cli) showKey(k *core.NewKey) {
	where := "Its private part stays on this machine, in " + ui.Tilde(k.Dir) + ", and never enters the sandbox."
	if k.Inside {
		where = "Its private part is stored INSIDE the sandbox, because " + k.Why +
			": anything running there can read and copy it. Authorize it only where that's acceptable."
	}
	body := x.t.Bold.Render("New disposable SSH key for '"+k.Sandbox+"'") + "\n" +
		lipgloss.Wrap(where+" It's deleted with the sandbox; remove it from authorized_keys then too.", 72, " ") + "\n\n" +
		x.t.Dim.Render("Authorize it where the sandbox may log in, e.g. ~/.ssh/authorized_keys on a server:") + "\n" +
		k.PublicKey + "\n\n" +
		x.t.Dim.Render(k.Fingerprint+" · print it again with: opencode-sandbox key "+k.Sandbox)
	if !ui.IsTerminal(os.Stderr) {
		fmt.Fprintf(os.Stderr, "\nNew disposable SSH key for '%s'. %s\n%s\nPrint it again with: opencode-sandbox key %s\n\n", k.Sandbox, where, k.PublicKey, k.Sandbox)
		return
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(x.t.Accent).Padding(0, 1)
	lipgloss.Fprintln(os.Stderr, box.Render(body))
	if x.stdinTTY {
		lipgloss.Fprint(os.Stderr, x.t.Dim.Render("Press enter to open the sandbox… "))
		waitEnter()
	}
}

// waitEnter waits for enter on the terminal.
func waitEnter() {
	var b [1]byte
	for {
		if n, err := os.Stdin.Read(b[:]); n == 0 || err != nil || b[0] == '\n' || b[0] == '\r' {
			return
		}
	}
}

// openNow opens a prepared sandbox on this terminal.
func (x *cli) openNow(name string) (int, error) {
	x.rep.Done()
	code, err := x.c.Open(name)
	if errors.Is(err, core.ErrHangup) {
		return 129, nil
	}
	return code, err
}

func (x *cli) ls(args []string) (int, error) {
	if len(args) > 0 {
		return 0, usagef("ls takes no arguments")
	}
	x.c.CollectAgents()
	ss, err := x.c.Sandboxes()
	if err != nil {
		return 0, err
	}
	if len(ss) == 0 {
		fmt.Println("no sandboxes (create one with: opencode-sandbox new DIR)")
		return 0, nil
	}
	x.out(x.t.Table(ss))
	for _, s := range ss {
		if s.KeyInside {
			x.out(x.t.Dim.Render("* private key stored inside the sandbox (no host ssh-agent where it runs)"))
			break
		}
	}
	for _, s := range ss {
		if s.APIKeysInside {
			x.out(x.t.Dim.Render("* API key stored inside the sandbox (made before 0.9.0 or with the gateway off, or one the gateway can't keep: see opencode-sandbox health)"))
			break
		}
	}
	return 0, nil
}

func (x *cli) open(args []string) (int, error) {
	if len(args) > 1 {
		return 0, usagef("open takes one sandbox name")
	}
	name := first(args)
	if name == "" {
		if !x.stdinTTY {
			return 0, usagef("open needs a sandbox name (see: opencode-sandbox ls)")
		}
		ss, err := x.c.Sandboxes()
		if err != nil {
			return 0, err
		}
		if len(ss) == 0 {
			return 0, errors.New("no sandboxes yet; create one with: opencode-sandbox new DIR")
		}
		var opts []ui.Option
		for _, s := range ss {
			opts = append(opts, ui.Option{Value: s.Name, Label: s.Name, Detail: ui.Status(s) + " · " + s.RunsIn + " · " + ui.Source(s)})
		}
		if name, err = x.t.Pick("Open which sandbox?", "", opts); err != nil {
			return 0, err
		}
	}
	if err := x.c.Prepare(name); err != nil {
		return 0, err
	}
	return x.openNow(name)
}

func (x *cli) shell(args []string) (int, error) {
	if len(args) != 1 {
		return 0, usagef("shell needs one sandbox name (see: opencode-sandbox ls)")
	}
	argv, err := x.c.ShellArgv(args[0])
	if err != nil {
		return 0, err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, err
	}
	if os.Getenv("OPENCODE_SANDBOX_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "+ %s\n", run.Quote(argv))
	}
	return 0, syscall.Exec(path, argv, os.Environ())
}

func (x *cli) key(args []string) (int, error) {
	if len(args) != 1 {
		return 0, usagef("key needs one sandbox name (see: opencode-sandbox ls)")
	}
	pub, err := x.c.PubKey(args[0])
	if err != nil {
		return 0, err
	}
	fmt.Print(pub)
	return 0, nil
}

func (x *cli) cp(args []string) (int, error) {
	if len(args) < 2 {
		return 0, usagef("usage: opencode-sandbox cp NAME PATH...")
	}
	return 0, x.c.CopyIn(args[0], args[1:], func(src string) {
		x.done(fmt.Sprintf("copied %s into %s:/workspace/", src, args[0]))
	})
}

func (x *cli) get(args []string) (int, error) {
	if len(args) < 2 {
		return 0, usagef("usage: opencode-sandbox get NAME PATH...")
	}
	here, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	return 0, x.c.CopyOut(args[0], args[1:], here, func(src, dst string) {
		x.done(fmt.Sprintf("copied %s:%s to %s", args[0], src, dst))
	})
}

func (x *cli) rm(args []string) (int, error) {
	all, yes := false, false
	var names []string
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "-y", "--yes":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				return 0, usagef("unknown option for rm: %s", a)
			}
			names = append(names, a)
		}
	}
	switch {
	case all && len(names) > 0:
		return 0, usagef("rm takes names or --all, not both")
	case !all && len(names) == 0:
		return 0, usagef("rm needs sandbox names, or --all")
	case all:
		var err error
		if names, err = x.c.Names(); err != nil {
			return 0, err
		}
		if len(names) == 0 {
			fmt.Println("no sandboxes")
			return 0, nil
		}
		if !yes {
			if !x.stdinTTY {
				return 0, usagef("rm --all deletes %d sandboxes; add -y to confirm without a terminal", len(names))
			}
			ok, err := x.t.Confirm(fmt.Sprintf("Delete all %d sandboxes?", len(names)),
				"Their files and disposable keys go too: "+strings.Join(names, ", "))
			if err != nil {
				return 0, err
			}
			if !ok {
				fmt.Println("nothing deleted")
				return 0, nil
			}
		}
	}
	for _, n := range names {
		if !x.c.Exists(n) {
			x.rep.Warn(fmt.Sprintf("no sandbox named '%s'", n))
			continue
		}
		r, err := x.c.Remove(n)
		if err != nil {
			return 0, err
		}
		if r.Disposable {
			x.done(fmt.Sprintf("removed %s and its disposable key %s (remove it from any authorized_keys you added it to)", n, r.KeyFingerprint))
		} else {
			x.done("removed " + n)
		}
	}
	return 0, nil
}

func (x *cli) build(args []string) (int, error) {
	x.rep.Stream = true
	x.rep.Step("Building the sandbox image")
	if err := x.c.Build(args); err != nil {
		return 0, err
	}
	x.rep.Done()
	return 0, nil
}

func (x *cli) agents(args []string) (int, error) {
	switch first(args) {
	case "":
	case "stop":
		x.c.StopAgents()
		x.done("all sandbox ssh-agents stopped")
		return 0, nil
	default:
		return 0, usagef("usage: opencode-sandbox agents [stop]")
	}
	as := x.c.Agents()
	if len(as) == 0 {
		fmt.Println("no sandbox agents running (they only run while a sandbox is open)")
		return 0, nil
	}
	for _, a := range as {
		x.out(x.t.Bold.Render(a.Sandbox) + x.t.Dim.Render(" ("+ui.Tilde(a.Key)+")"))
		for _, l := range a.Loaded {
			x.out("  " + l)
		}
	}
	return 0, nil
}

func (x *cli) health() (int, error) {
	d := health.Build(x.c.Health())
	if x.outTTY {
		x.out(x.t.Health(d, termWidth()))
	} else {
		health.Plain(os.Stdout, d)
	}
	if !d.OK {
		return 1, nil
	}
	return 0, nil
}

// update replaces this program with the latest release, or pulls the git
// clone it runs from.
func (x *cli) update(args []string) (int, error) {
	check := false
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "-h", "--help":
			x.help(os.Stdout, "update")
			return 0, nil
		default:
			return 0, usagef("update takes only --check (got %s)", a)
		}
	}
	exe, err := selfupdate.Executable()
	if err != nil {
		return 0, err
	}
	if c := selfupdate.CloneOf(exe); c != nil {
		return x.updateClone(c, check)
	}
	// ctrl-c stops a download; the program in place is untouched until the end.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	u := selfupdate.New(core.Version)
	x.rep.Step("Looking for the latest release")
	rel, err := u.Latest(ctx)
	if err != nil {
		return 0, fmt.Errorf("couldn't find the latest release: %w", err)
	}
	x.rep.Done()
	switch c := selfupdate.Compare(rel.Version, core.Version); {
	case c == 0:
		x.done(fmt.Sprintf("opencode-sandbox %s is the latest release", core.Version))
		return 0, nil
	case c < 0:
		x.done(fmt.Sprintf("opencode-sandbox %s is newer than the latest release (%s)", core.Version, rel.Version))
		return 0, nil
	case check:
		fmt.Printf("opencode-sandbox %s is out (you have %s); get it with: opencode-sandbox update\n", rel.Version, core.Version)
		return 0, nil
	}
	x.rep.Step("Downloading opencode-sandbox " + rel.Version)
	if err := u.Install(ctx, rel, exe); err != nil {
		var na *selfupdate.NoAccessError
		if errors.As(err, &na) {
			return 0, fmt.Errorf("can't write to %s, where this opencode-sandbox is; update it as someone who can (sudo %s update), or install a copy of your own with the install script (see %s)",
				na.Dir, exe, strings.TrimSuffix(selfupdate.Releases, "/releases")+"#install")
		}
		return 0, err
	}
	x.rep.Done()
	x.done(fmt.Sprintf("updated opencode-sandbox %s → %s (%s)", core.Version, rel.Version, ui.Tilde(exe)))
	x.rep.Note("what changed: " + rel.Page())
	x.rep.Note("your sandboxes keep the image they were created with; new ones use " + rel.Version + "'s, built the first time")
	return 0, nil
}

// updateClone updates the git clone this program was built by.
func (x *cli) updateClone(c *selfupdate.Clone, check bool) (int, error) {
	where := ui.Tilde(c.Root)
	if _, err := exec.LookPath("git"); err != nil {
		return 0, fmt.Errorf("this opencode-sandbox runs from a git clone (%s), and updating it needs git", where)
	}
	branch := c.Branch()
	if branch == "" {
		return 0, fmt.Errorf("this opencode-sandbox runs from a git clone (%s) that's at a fixed commit, not on a branch; move it yourself, e.g.: git -C %s checkout master && git -C %s pull",
			where, c.Root, c.Root)
	}
	if check {
		x.rep.Step("Fetching " + where)
		n, err := c.Behind()
		if err != nil {
			return 0, err
		}
		x.rep.Done()
		if n == 0 {
			x.done(fmt.Sprintf("the clone in %s is up to date (%s)", where, branch))
		} else {
			fmt.Printf("the clone in %s is %d commits behind (%s); get them with: opencode-sandbox update\n", where, n, branch)
		}
		return 0, nil
	}
	x.rep.Note(fmt.Sprintf("this opencode-sandbox runs from a git clone (%s): updating it with git pull --ff-only", where))
	before, err := c.Head()
	if err != nil {
		return 0, err
	}
	if err := c.Pull(os.Stdin, os.Stderr); err != nil {
		return 0, fmt.Errorf("git couldn't update the clone in %s (it says why above); nothing was changed", where)
	}
	if after, err := c.Head(); err != nil {
		return 0, err
	} else if after == before {
		x.done(fmt.Sprintf("nothing new: still opencode-sandbox %s", core.Version))
		return 0, nil
	}
	x.rep.Step("Building the new version")
	v, err := c.Rebuild(x.rep.Log())
	if err != nil {
		return 0, fmt.Errorf("the clone was updated, but building it failed: %w", err)
	}
	x.rep.Done()
	x.done("updated to " + v)
	return 0, nil
}
