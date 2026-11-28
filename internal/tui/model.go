// Package tui is opencode-sandbox's dashboard: the sandboxes, kept up to date,
// with keys to create, open, copy into and out of, and delete them. Opening
// one hands the terminal to opencode and comes back when it closes or
// detaches.
package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

type mode int

const (
	listMode     mode = iota
	formMode          // the new-sandbox form
	progressMode      // creating a sandbox or building the image
	keyMode           // a public key
	confirmMode       // delete?
	inputMode         // a path to copy in or out
	healthMode        // the health report
)

type kind int

const (
	info kind = iota
	good
	warn
	bad
)

// Run shows the dashboard until the user quits and returns the exit status.
// setupErr is why no engine could be found, if none could: the dashboard
// then shows the health report, with what to install.
func Run(c *core.Core, t *ui.Theme, setupErr error) int {
	m := newModel(c, t, setupErr)
	// Where there's something to say first (no engine), it isn't kept waiting.
	m.intro = setupErr == nil && introWanted()
	prevRep, prevStderr := c.Rep, run.Stderr
	c.Rep = newReporter(m.ev)
	run.Stderr = &lines{emit: func(l string) { m.ev <- noteMsg{text: l, warn: true} }}
	defer func() { c.Rep, run.Stderr = prevRep, prevStderr }()
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencode-sandbox:", err)
		return 1
	}
	return m.code
}

type model struct {
	c        *core.Core
	t        *ui.Theme
	ev       events
	setupErr error
	w, h     int

	all, rows []core.Sandbox // as listed, and after the filter
	loaded    bool
	cursor    int
	marked    map[string]bool
	goTo      string            // a sandbox to move to once it's listed
	fps       map[string]string // key fingerprints, looked up lazily
	agents    map[string]bool
	// gateways: the open sandboxes whose gateway is being served.
	gateways map[string]bool

	filter    textinput.Model
	filtering bool

	mode mode
	spin spinner.Model
	busy string // what's running in the background, if anything

	status     string
	statusKind kind

	showHelp  bool
	quitArmed bool // ctrl+c was pressed once in the list

	// intro: the intro is playing, at introFrame (see intro.go).
	intro      bool
	introFrame int

	form *huh.Form
	nf   *newForm
	prog progress
	key  keyView
	del  []string // sandboxes to delete
	in   pathInput
	vp   viewport.Model

	code int // exit status
}

type progress struct {
	title, current string
	done, notes    []string
	log            []string
	err            error
	finished       bool
}

type keyView struct {
	title, body, pub, fp string
	openAfter            string // sandbox to open on enter (just created)
}

type pathInput struct {
	op, sandbox string // "cp" or "get"
	ti          textinput.Model
}

func newModel(c *core.Core, t *ui.Theme, setupErr error) *model {
	f := textinput.New()
	f.Prompt = "/ "
	f.Placeholder = "filter by name, folder or provider"
	m := &model{
		c: c, t: t, ev: make(events, 1024), setupErr: setupErr,
		marked: map[string]bool{}, fps: map[string]string{}, agents: map[string]bool{}, gateways: map[string]bool{},
		filter: f,
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(t.Accented)),
	}
	if setupErr != nil {
		m.mode = healthMode
	}
	return m
}

// --- messages ----------------------------------------------------------------

type (
	rowsMsg struct {
		ss  []core.Sandbox
		err error
	}
	tickMsg struct{}
	fpMsg   struct{ name, fp string }
	opMsg   struct {
		ok  string
		err error
	}
	preparedMsg struct {
		name string
		err  error
	}
	openedMsg struct {
		name     string
		code     int
		detached bool
		err      error
	}
	shellMsg struct {
		name string
		err  error
	}
	createdMsg struct {
		c   *core.Created
		err error
	}
	healthMsg struct{ d health.Doc }
	builtMsg  struct{ err error }
	pubMsg    struct {
		name, pub string
		err       error
	}
)

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{listen(m.ev), m.spin.Tick, tea.RequestBackgroundColor}
	if m.intro {
		cmds = append(cmds, introNext())
	}
	if m.setupErr != nil {
		cmds = append(cmds, m.loadHealth())
	} else {
		cmds = append(cmds, m.load())
	}
	return tea.Batch(cmds...)
}

func (m *model) load() tea.Cmd {
	c, busy := m.c, m.busy != ""
	return func() tea.Msg {
		if !busy {
			c.CollectAgents()
		}
		ss, err := c.Sandboxes()
		return rowsMsg{ss, err}
	}
}

func (m *model) loadHealth() tea.Cmd {
	c := m.c
	return func() tea.Msg { return healthMsg{health.Build(c.Health())} }
}

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

// background runs f as the one background operation, showing what; nil if
// one is running already.
func (m *model) background(what string, f func() tea.Msg) tea.Cmd {
	if m.isBusy() {
		return nil
	}
	m.busy = what
	return func() tea.Msg { return f() }
}

// isBusy says so when a background operation is running.
func (m *model) isBusy() bool {
	if m.busy == "" {
		return false
	}
	m.say(warn, "wait until it's done %s", lowerFirst(m.busy))
	return true
}

// quit leaves the dashboard, unless something would be left half done.
func (m *model) quit() (tea.Model, tea.Cmd) {
	if m.isBusy() {
		return m, nil
	}
	return m, tea.Quit
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (m *model) say(k kind, format string, args ...any) {
	m.status, m.statusKind = fmt.Sprintf(format, args...), k
}

// --- update ------------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The intro plays over the dashboard, which loads meanwhile: its frames
	// are the intro's, a key ends it, the rest is the dashboard's.
	switch msg := msg.(type) {
	case introMsg:
		if !m.intro {
			return m, nil
		}
		if m.introFrame++; m.introFrame >= introFrames {
			m.intro = false
			return m, nil
		}
		return m, introNext()
	case tea.KeyPressMsg:
		if m.intro {
			m.intro = false
			switch msg.String() {
			case "enter", "space", "esc":
				return m, nil // the keys one skips things with do no more
			}
			// Any other also does its job: n typed at once opens the form.
		}
	case tea.WindowSizeMsg:
		if msg.Width < introW || msg.Height < introH {
			m.intro = false // no room for it
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.vp.SetWidth(m.w - 4)
		m.vp.SetHeight(max(m.h-6, 3))
		if m.mode == healthMode && m.vp.TotalLineCount() > 0 {
			return m, m.loadHealth() // rewrap
		}
		return m, m.sizeForm()
	case tea.BackgroundColorMsg:
		if dark := msg.IsDark(); dark != m.t.Dark {
			*m.t = *ui.NewTheme(dark)
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case noteMsg:
		if m.mode == progressMode {
			m.prog.notes = append(m.prog.notes, prefix(msg.warn)+msg.text)
		} else if msg.warn {
			m.say(warn, "%s", msg.text)
		} else {
			m.say(info, "%s", msg.text)
		}
		return m, listen(m.ev)
	case stepMsg:
		if m.prog.current != "" {
			m.prog.done = append(m.prog.done, m.prog.current)
		}
		m.prog.current = msg.title
		return m, listen(m.ev)
	case stepDoneMsg:
		if m.prog.current != "" {
			m.prog.done = append(m.prog.done, m.prog.current)
			m.prog.current = ""
		}
		return m, listen(m.ev)
	case logMsg:
		m.prog.log = append(m.prog.log, msg.line)
		if len(m.prog.log) > 200 {
			m.prog.log = m.prog.log[len(m.prog.log)-200:]
		}
		return m, listen(m.ev)
	case rowsMsg:
		if msg.err != nil {
			m.say(bad, "%v", msg.err)
		} else {
			m.setRows(msg.ss)
		}
		m.loaded = true
		return m, tea.Batch(tick(), m.lookupCurrent())
	case tickMsg:
		return m, m.load()
	case fpMsg:
		m.fps[msg.name] = msg.fp
		return m, nil
	case healthMsg:
		m.vp.SetContent(m.t.Health(msg.d, m.w-6))
		return m, nil
	case opMsg:
		m.busy = ""
		if msg.err != nil {
			m.say(bad, "%v", msg.err)
		} else if msg.ok != "" {
			m.say(good, "%s", msg.ok)
		}
		m.keyDone()
		return m, m.load()
	case pubMsg:
		if msg.err != nil {
			m.say(bad, "%v", msg.err)
			return m, nil
		}
		m.key = keyView{title: "Public key of '" + msg.name + "'", pub: strings.TrimSpace(msg.pub), fp: m.fps[msg.name],
			body: "Authorize it where the sandbox may log in, e.g. in ~/.ssh/authorized_keys on a server."}
		m.mode = keyMode
		return m, nil
	case preparedMsg:
		m.busy = ""
		if msg.err != nil {
			m.say(bad, "%v", msg.err)
			return m, m.load()
		}
		return m, m.exec(msg.name)
	case openedMsg:
		switch {
		case errors.Is(msg.err, core.ErrHangup):
			m.code = 129
			return m, tea.Quit
		case msg.err != nil:
			m.say(bad, "%v", msg.err)
		case msg.detached:
			m.say(info, "'%s' is still open in the background; enter attaches to it again", msg.name)
		case msg.code != 0 && msg.code != 130:
			m.say(warn, "'%s' closed; opencode exited with status %d", msg.name, msg.code)
		default:
			m.say(good, "'%s' closed", msg.name)
		}
		return m, m.load()
	case shellMsg:
		if msg.err != nil {
			m.say(bad, "shell in '%s': %v", msg.name, msg.err)
		}
		return m, m.load()
	case builtMsg:
		watching := m.finish(msg.err)
		if msg.err == nil {
			if watching {
				m.mode = listMode
			}
			m.say(good, "image built; new sandboxes use it")
		}
		return m, nil
	case createdMsg:
		watching := m.finish(msg.err)
		if msg.err != nil {
			return m, m.load()
		}
		if !watching {
			// It was left to finish in the background: say so, and point at
			// it, rather than take the screen over now.
			hint := "enter opens it"
			if msg.c.NewKey != nil {
				hint = "p shows its new key, enter opens it"
			}
			m.goTo = msg.c.Name
			m.say(good, "created '%s'; %s", msg.c.Name, hint)
			return m, m.load()
		}
		m.mode = listMode
		m.goTo = msg.c.Name
		m.say(good, "created '%s'", msg.c.Name)
		if k := msg.c.NewKey; k != nil {
			body := "Its private part stays on this machine and never enters the sandbox."
			if k.Inside {
				body = "Its private part is stored INSIDE the sandbox, because " + k.Why + ": anything running there can read and copy it. Authorize it only where that's acceptable."
			}
			m.key = keyView{title: "New disposable SSH key for '" + k.Sandbox + "'", pub: k.PublicKey, fp: k.Fingerprint,
				body: body + " It's deleted with the sandbox; remove it from authorized_keys then too.", openAfter: k.Sandbox}
			m.mode = keyMode
			return m, m.load()
		}
		return m, m.exec(msg.c.Name)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.mode == formMode && m.form != nil {
		return m.updateForm(msg)
	}
	if m.mode == inputMode {
		var cmd tea.Cmd
		m.in.ti, cmd = m.in.ti.Update(msg)
		return m, cmd
	}
	return m, nil
}

// finish ends the operation the progress view shows, and reports whether
// the user is still watching it. A failure that nobody watched is shown
// anyway: in the progress view from the list, else on the status line.
func (m *model) finish(err error) (watching bool) {
	watching = m.mode == progressMode
	m.busy = ""
	m.prog.finished, m.prog.err = true, err
	if m.prog.current != "" {
		m.prog.done = append(m.prog.done, m.prog.current)
		m.prog.current = ""
	}
	if err != nil && !watching {
		if m.mode == listMode && !m.filtering {
			m.mode = progressMode
		} else {
			m.say(bad, "%v", err)
		}
	}
	return watching
}

func prefix(warn bool) string {
	if warn {
		return "! "
	}
	return "› "
}

// keyDone leaves a finished operation's mode.
func (m *model) keyDone() {
	if m.mode == progressMode && m.prog.finished && m.prog.err == nil {
		m.mode = listMode
	}
}

func (m *model) setRows(ss []core.Sandbox) {
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Name < ss[j].Name })
	cur := m.current()
	m.all = ss
	m.applyFilter()
	for i, s := range m.rows {
		if cur != nil && s.Name == cur.Name {
			m.cursor = i
		}
	}
	for i, s := range m.rows {
		if s.Name == m.goTo {
			m.cursor, m.goTo = i, ""
		}
	}
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
	for n := range m.marked {
		if !m.has(n) {
			delete(m.marked, n)
		}
	}
	for _, s := range ss {
		m.agents[s.Name] = s.State == core.StateOpen && !s.KeyInside && s.KeyKind != "none" && m.c.AgentRunning(s.Name)
		m.gateways[s.Name] = s.State == core.StateOpen && s.Gateway && m.c.GatewayRunning(s.Name)
	}
}

func (m *model) has(name string) bool {
	for _, s := range m.all {
		if s.Name == name {
			return true
		}
	}
	return false
}

func (m *model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.rows = m.rows[:0]
	for _, s := range m.all {
		if q == "" || strings.Contains(strings.ToLower(s.Name+" "+s.Source+" "+s.Providers+" "+s.RunsIn), q) {
			m.rows = append(m.rows, s)
		}
	}
}

func (m *model) current() *core.Sandbox {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		s := m.rows[m.cursor]
		return &s
	}
	return nil
}

// lookupCurrent fetches the current sandbox's key fingerprint once.
func (m *model) lookupCurrent() tea.Cmd {
	s := m.current()
	if s == nil || s.KeyKind == "none" {
		return nil
	}
	if _, ok := m.fps[s.Name]; ok {
		return nil
	}
	m.fps[s.Name] = ""
	c, sb := m.c, *s
	return func() tea.Msg { return fpMsg{sb.Name, c.Fingerprint(sb)} }
}

// --- keys --------------------------------------------------------------------

func (m *model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	m.status = "" // what was said answered the previous key
	armed := m.quitArmed
	m.quitArmed = false
	if s == "ctrl+c" {
		if m.mode == listMode && !m.filtering {
			// Twice to quit: the ctrl+c that closes opencode is easily
			// pressed once too often, and would land here.
			if armed {
				return m.quit()
			}
			m.quitArmed = true
			m.say(info, "press ctrl+c again to quit (or q)")
			return m, nil
		}
		// Inside a form or dialog, ctrl+c backs out of it, like esc.
		s, k = "esc", tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	switch m.mode {
	case formMode:
		// esc leaves the form, unless the field uses it (to end a filter).
		if s == "esc" && !formUses(m.form, "esc") {
			m.mode, m.form = listMode, nil
			m.say(info, "no sandbox created")
			return m, nil
		}
		return m.updateForm(k)
	case progressMode:
		switch {
		case m.prog.finished && (s == "esc" || s == "enter" || s == "q"):
			m.mode = listMode
		case !m.prog.finished && s == "esc":
			// It can't be cancelled halfway, but needn't be watched.
			m.mode = listMode
			m.say(info, "still %s; you'll see here when it's done", lowerFirst(m.busy))
		}
		return m, nil
	case keyMode:
		switch s {
		case "y":
			m.say(good, "copied the public key to the clipboard (where the terminal allows it)")
			return m, tea.SetClipboard(m.key.pub)
		case "enter":
			m.mode = listMode
			if n := m.key.openAfter; n != "" {
				return m, m.exec(n)
			}
		case "esc", "q":
			m.mode = listMode
		}
		return m, nil
	case confirmMode:
		switch s {
		case "y", "Y":
			m.mode = listMode
			return m, m.remove(m.del)
		case "n", "N", "esc", "q":
			m.mode = listMode
			m.say(info, "nothing deleted")
		}
		return m, nil
	case inputMode:
		switch s {
		case "esc":
			m.mode = listMode
			return m, nil
		case "enter":
			m.mode = listMode
			return m, m.copy()
		case "tab":
			if m.in.op == "cp" {
				m.in.ti.SetValue(complete(m.in.ti.Value()))
				m.in.ti.CursorEnd()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.in.ti, cmd = m.in.ti.Update(k)
		return m, cmd
	case healthMode:
		switch s {
		case "esc", "q", "h":
			if m.setupErr != nil {
				return m, tea.Quit
			}
			m.mode = listMode
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}

	if m.filtering {
		switch s {
		case "up":
			m.cursor = max(m.cursor-1, 0)
			return m, m.lookupCurrent()
		case "down":
			m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
			return m, m.lookupCurrent()
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
		case "enter":
			m.filtering = false
			m.filter.Blur()
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(k)
			m.applyFilter()
			m.cursor = 0
			return m, cmd
		}
		m.applyFilter()
		return m, nil
	}

	cur := m.current()
	switch s {
	case "q":
		return m.quit()
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(m.rows)-1, 0)
	case "pgup":
		m.cursor = max(m.cursor-10, 0)
	case "pgdown":
		m.cursor = min(m.cursor+10, max(len(m.rows)-1, 0))
	case "?":
		m.showHelp = !m.showHelp
	case "/":
		m.filtering = true
		return m, m.filter.Focus()
	case "esc":
		m.marked = map[string]bool{}
		m.filter.SetValue("")
		m.applyFilter()
	case "r":
		return m, m.load()
	case "h":
		m.mode = healthMode
		m.vp.SetContent(m.t.Dim.Render("checking…"))
		return m, m.loadHealth()
	case "space", " ":
		if cur != nil {
			m.marked[cur.Name] = !m.marked[cur.Name]
			if !m.marked[cur.Name] {
				delete(m.marked, cur.Name)
			}
			m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
		}
	case "n":
		return m, m.startForm()
	case "b":
		if m.isBusy() {
			return m, nil
		}
		if err := m.c.CanCreate(); err != nil {
			m.say(bad, "%v", err)
			return m, nil
		}
		m.prog = progress{title: "Building the sandbox image"}
		m.mode = progressMode
		c := m.c
		return m, m.background("Building the image", func() tea.Msg {
			c.Rep.Step("Building the sandbox image")
			err := c.Build(nil)
			c.Rep.Done()
			return builtMsg{err}
		})
	}
	if cur == nil {
		return m, m.lookupCurrent()
	}
	switch s {
	case "enter", "o":
		if m.isBusy() {
			return m, nil
		}
		if cur.State == core.StateOpen && cur.Engine == "apple" {
			m.say(warn, "'%s' is open in another terminal, and Apple container can't attach a second one; s opens a shell in it", cur.Name)
			return m, nil
		}
		name, c := cur.Name, m.c
		return m, m.background("Preparing '"+name+"'", func() tea.Msg { return preparedMsg{name, c.Prepare(name)} })
	case "s":
		// Not while something runs in the background: the dashboard stands
		// still while the terminal is the shell's.
		if m.isBusy() {
			return m, nil
		}
		if cur.State != core.StateOpen {
			m.say(warn, "'%s' isn't open; enter opens it", cur.Name)
			return m, nil
		}
		argv, err := m.c.ShellArgv(cur.Name)
		if err != nil {
			m.say(bad, "%v", err)
			return m, nil
		}
		name := cur.Name
		return m, tea.Exec(&shellCommand{exec.Command(argv[0], argv[1:]...)}, func(err error) tea.Msg { return shellMsg{name, err} })
	case "p":
		name, c := cur.Name, m.c
		return m, func() tea.Msg { pub, err := c.PubKey(name); return pubMsg{name, pub, err} }
	case "c", "g":
		if m.isBusy() {
			return m, nil
		}
		m.in = pathInput{op: map[string]string{"c": "cp", "g": "get"}[s], sandbox: cur.Name, ti: textinput.New()}
		if s == "c" {
			m.in.ti.Prompt = "copy into " + cur.Name + ":/workspace/  from "
			m.in.ti.Placeholder = "a file or folder on this machine (tab completes)"
		} else {
			wd, _ := os.Getwd()
			m.in.ti.Prompt = "copy " + cur.Name + ":/workspace/"
			m.in.ti.Placeholder = "path in the sandbox"
			m.in.ti.Placeholder += "  → " + ui.Tilde(wd)
		}
		m.mode = inputMode
		return m, m.in.ti.Focus()
	case "d", "x", "delete":
		if m.isBusy() {
			return m, nil
		}
		m.del = nil
		for n := range m.marked {
			m.del = append(m.del, n)
		}
		if len(m.del) == 0 {
			m.del = []string{cur.Name}
		}
		sort.Strings(m.del)
		m.mode = confirmMode
		return m, nil
	}
	return m, m.lookupCurrent()
}

// exec hands the terminal to the sandbox until it closes or detaches.
func (m *model) exec(name string) tea.Cmd {
	o := m.c.OpenCommand(name)
	hint := "quit opencode to come back here; ctrl-] ctrl-] detaches and leaves it running"
	for _, s := range m.all {
		if s.Name == name && s.Engine == "apple" {
			hint = "quit opencode to come back here"
		}
	}
	b := banner{o, m.t.Accented.Render("›") + " opening '" + name + "' · " + m.t.Dim.Render(hint)}
	return tea.Exec(b, func(err error) tea.Msg { return openedMsg{name, o.Code, o.Detached, err} })
}

// banner prints a line before opening the sandbox on the bare terminal.
type banner struct {
	*core.OpenCommand
	line string
}

func (b banner) SetStdout(w io.Writer) {
	b.OpenCommand.SetStdout(w)
	lipgloss.Fprintln(w, b.line)
}

func (m *model) remove(names []string) tea.Cmd {
	c := m.c
	return m.background("Deleting", func() tea.Msg {
		var gone, keys []string
		for _, n := range names {
			r, err := c.Remove(n)
			if err != nil {
				return opMsg{err: fmt.Errorf("deleting %s: %w", n, err)}
			}
			gone = append(gone, n)
			if r.Disposable {
				keys = append(keys, r.KeyFingerprint)
			}
		}
		msg := "deleted " + strings.Join(gone, ", ")
		if len(keys) > 0 {
			msg += " and disposable key " + strings.Join(keys, ", ") + " (remove it from any authorized_keys you added it to)"
		}
		return opMsg{ok: msg}
	})
}

func (m *model) copy() tea.Cmd {
	in, c := m.in, m.c
	p := strings.TrimSpace(in.ti.Value())
	if p == "" {
		return nil
	}
	if in.op == "cp" {
		p = expand(p)
		return m.background("Copying into '"+in.sandbox+"'", func() tea.Msg {
			err := c.CopyIn(in.sandbox, []string{p}, nil)
			return opMsg{ok: fmt.Sprintf("copied %s into %s:/workspace/", ui.Tilde(p), in.sandbox), err: err}
		})
	}
	return m.background("Copying out of '"+in.sandbox+"'", func() tea.Msg {
		wd, err := os.Getwd()
		if err != nil {
			return opMsg{err: err}
		}
		var dst string
		err = c.CopyOut(in.sandbox, []string{p}, wd, func(_, d string) { dst = d })
		return opMsg{ok: fmt.Sprintf("copied %s:%s to %s", in.sandbox, p, ui.Tilde(dst)), err: err}
	})
}

// expand turns ~/… into a path in your home folder.
func expand(p string) string {
	if home, err := os.UserHomeDir(); err == nil && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, p[1:])
	}
	return p
}

// complete extends a path to the longest name it starts uniquely. It splits
// the path as typed, not as expanded: "~" and "~/src/" expand to paths that
// end in another name.
func complete(p string) string {
	if p == "~" {
		return "~/"
	}
	head, base := filepath.Split(p)
	dir := expand(head)
	if dir == "" {
		dir = "."
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), base) && (strings.HasPrefix(base, ".") || !strings.HasPrefix(e.Name(), ".")) {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return p
	}
	common := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, common) {
			common = common[:len(common)-1]
		}
	}
	return head + common
}
