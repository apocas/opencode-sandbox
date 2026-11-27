package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/setup"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// setup installs what gives new sandboxes the strongest isolation this
// machine can have, after showing what it will do.
func (x *cli) setup(args []string) (int, error) {
	yes := false
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		case "-h", "--help":
			x.help(os.Stdout, "setup")
			return 0, nil
		default:
			return 0, usagef("setup takes only -y (got %s)", a)
		}
	}
	if os.Getuid() == 0 {
		return 0, errors.New("run setup as yourself, not as root: it looks at what you can use, and runs the steps that need root with sudo")
	}
	f := x.c.Health()
	plan := health.PlanFor(f)
	if len(plan.Steps) == 0 {
		if plan.Best {
			x.done("nothing to set up: " + plan.Note)
			return 0, nil
		}
		x.out(x.wrap(plan.Note, 0))
		if f.Engine == "" { // and sandboxes can't run as things are
			return 1, nil
		}
		return 0, nil
	}
	x.showPlan(plan)
	if plan.Sudo() {
		if _, err := exec.LookPath("sudo"); err != nil {
			return 0, errors.New("some of these steps need root and there's no sudo here; run the commands above yourself, as root where they say sudo")
		}
	}
	if !yes {
		if !x.stdinTTY {
			return 0, usagef("setup changes this machine, so it asks first, and there's no terminal to ask on; add -y to go ahead")
		}
		ok, err := x.t.Confirm("Go ahead?", "")
		if err != nil {
			return 0, err
		}
		if !ok {
			fmt.Println("nothing was changed")
			return 0, nil
		}
	}

	// ctrl-c stops a download; a command that's running gets it too.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &setup.Setup{
		Run: run.OS{}, Client: &http.Client{}, Progress: &setupProgress{x: x},
		Stdin: os.Stdin, Out: os.Stderr, Machine: f.Machine,
		Apple: setup.AppleReleases, Gvisor: setup.GvisorReleases, Agent: "opencode-sandbox/" + core.Version,
	}
	if err := s.Apply(ctx, plan); err != nil {
		return 0, err
	}
	x.rep.Stream = false

	// What does this machine have now? Docker reads its configuration a
	// moment after it's told to.
	var now health.Facts
	var after health.Plan
	for i := 0; ; i++ {
		now = core.New().Health()
		after = health.PlanFor(now)
		if len(after.Steps) == 0 || i == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	switch {
	case len(after.Steps) > 0:
		hint := ""
		if f.GOOS == "linux" {
			hint = " (if Docker doesn't list the new runtime, it may read another configuration than /etc/docker/daemon.json, as a snap's does)"
		}
		return 0, fmt.Errorf("the steps ran, but %s still isn't in place%s; `opencode-sandbox health` shows what this machine has now", plan.What, hint)
	case after.Best:
		if err := x.tryGvisor(now); err != nil {
			return 0, err
		}
		x.done(strings.Replace(after.Note, "already ", "now ", 1))
		x.rep.Note("sandboxes you already have stay as they are")
	default: // set up, but something keeps new sandboxes off it
		x.rep.Warn(after.Note)
	}
	return 0, nil
}

// tryGvisor starts a container on the gVisor that new sandboxes now use,
// where the image is there to try with: one that can't start here would fail
// every new sandbox.
func (x *cli) tryGvisor(f health.Facts) error {
	if f.Engine != "docker" || f.Docker == nil || !f.ImageOK {
		return nil
	}
	rt := f.Runtime
	if rt == "" {
		rt = f.Docker.DefaultRuntime
	}
	if platform.Kind(*f.Docker, rt) != platform.Gvisor {
		return nil
	}
	x.rep.Step("Starting a test container on gVisor")
	out, err := exec.Command("docker", "run", "--rm", "--runtime", rt, "--network", "none", "--entrypoint", "true", f.Image).CombinedOutput()
	if err != nil {
		said := strings.TrimSpace(string(out))
		if said == "" {
			said = err.Error()
		}
		return fmt.Errorf("gVisor is set up, but a test container didn't start on it: %s. New sandboxes would fail the same way; until that's sorted out, set %s=%s to keep them on plain Docker",
			said, platform.RuntimeEnv, "runc")
	}
	x.rep.Done()
	return nil
}

// showPlan prints what setup is about to do.
func (x *cli) showPlan(p health.Plan) {
	t := x.t
	var b strings.Builder
	b.WriteString(t.Title.Render("Setting up "+p.What) + "\n")
	b.WriteString(x.wrap("So that "+p.Gain+".", 0) + "\n\n")
	for i, s := range p.Steps {
		num := fmt.Sprintf("%2d. ", i+1)
		b.WriteString(t.Accented.Render(num) + x.wrap(s.Title, len(num)) + "\n")
		for _, c := range s.Cmds {
			b.WriteString(strings.Repeat(" ", len(num)+2) + t.Dim.Render(setup.Shown(c)) + "\n")
		}
	}
	b.WriteString("\n")
	notes := "Sandboxes you already have aren't changed; new ones use it by themselves."
	if p.Sudo() {
		notes = "sudo asks for your password. " + notes
	}
	if p.After != "" {
		notes += " Afterwards: " + p.After
	}
	b.WriteString(x.wrap(notes, 0))
	x.out(b.String())
}

// wrap breaks text at the terminal's width (at most 100 columns), with the
// lines after the first indented by indent.
func (x *cli) wrap(text string, indent int) string {
	width := min(termWidth(), 100) - indent
	lines := strings.Split(lipgloss.Wrap(text, max(width, 30), " "), "\n")
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}

// setupProgress shows setup's steps as they're done: a spinner for a
// download, the commands themselves for the rest (sudo may ask for a
// password between them).
type setupProgress struct {
	x    *cli
	last string
}

var fetching = map[string]string{
	health.FetchApple:  "Downloading Apple's installer and checking its signature",
	health.FetchGvisor: "Downloading gVisor and checking it",
}

func (p *setupProgress) title(i, n int, s health.Step) string {
	title := s.Title
	if s.Fetch != "" {
		title = fetching[s.Fetch] // short: a spinner's line mustn't wrap
	}
	return fmt.Sprintf("%d/%d %s", i, n, title)
}

func (p *setupProgress) Begin(i, n int, s health.Step) {
	p.x.rep.Stream = s.Fetch == ""
	p.last = ""
	p.x.rep.Step(p.title(i, n, s))
}

func (p *setupProgress) End(i, n int, s health.Step) { p.x.rep.Done() }

func (p *setupProgress) Command(shown string) {
	if ui.IsTerminal(os.Stderr) {
		lipgloss.Fprintln(os.Stderr, p.x.t.Dim.Render("    $ "+shown))
	} else {
		fmt.Fprintln(os.Stderr, "opencode-sandbox: $ "+shown)
	}
}

func (p *setupProgress) Downloaded(done, total int64) {
	text := fmt.Sprintf("%d MB", done>>20)
	if total > 0 {
		text = fmt.Sprintf("%d of %d MB", done>>20, total>>20)
	}
	if text != p.last {
		p.last = text
		p.x.rep.Detail(text)
	}
}
