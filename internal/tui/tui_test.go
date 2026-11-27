package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

func sample() []core.Sandbox {
	return []core.Sandbox{
		{Name: "api-7f3a", State: core.StateOpen, Status: "open (5 minutes)", Created: "2 hours ago", Engine: "apple", RunsIn: "apple",
			KeyKind: "disposable", KeyName: "disposable", KeyInside: true, Providers: "my-provider", Source: "/src/api", Version: core.Version},
		{Name: "web-12bc", State: core.StateClosed, Status: "closed 2 hours ago", Created: "3 days ago", Engine: "docker", RunsIn: "gvisor",
			KeyKind: "home", Key: "/h/.ssh/id_ed25519", KeyName: "id_ed25519", Providers: "anthropic,openai", Source: "/src/web", Version: "0.4.0"},
		{Name: "infra", State: core.StateNew, Status: "new", Created: "just now", Engine: "docker", RunsIn: "docker",
			KeyKind: "none", KeyName: "none", Providers: "", Source: "/ops"},
	}
}

// screen renders the model at w×h after msgs, without colors.
func screen(t *testing.T, m *model, w, h int, msgs ...tea.Msg) string {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range msgs {
		m.Update(msg)
	}
	return ansi.Strip(m.View().Content)
}

func newTest(t *testing.T) *model {
	t.Setenv("HOME", t.TempDir())
	return newModel(core.New(), ui.NewTheme(true), nil)
}

func press(s string) tea.KeyPressMsg {
	if s == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	if s == "esc" {
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func TestListAndDetails(t *testing.T) {
	m := newTest(t)
	out := screen(t, m, 120, 20, rowsMsg{ss: sample()})
	for _, want := range []string{"opencode-sandbox " + core.Version, "Sandboxes (3)", "› ", "api-7f3a", "open · 5 minutes",
		"web-12bc", "closed 2 hours ago", "gvisor", "infra", "new", "copied from  /src/api", "enter open", "n new", "q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	details := strings.Join(strings.Fields(ansi.Strip(m.details(46))), " ")
	for _, want := range []string{"a VM of its own (Apple container)", "disposable, stored inside the sandbox", "s shell · open in another terminal"} {
		if !strings.Contains(details, want) {
			t.Errorf("details: missing %q in %q", want, details)
		}
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if n := len(strings.Split(out, "\n")); n > 20 {
		t.Errorf("%d lines for a 20-line terminal", n)
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
}

func TestEmptyAndNarrow(t *testing.T) {
	m := newTest(t)
	out := screen(t, m, 70, 16, rowsMsg{})
	if !strings.Contains(out, "No sandboxes yet.") || !strings.Contains(out, "to create one") {
		t.Errorf("empty list:\n%s", out)
	}
	m = newTest(t)
	out = screen(t, m, 70, 16, rowsMsg{ss: sample()})
	if strings.Contains(out, "copied from") {
		t.Error("details shown on a narrow terminal")
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 70 {
			t.Errorf("too wide: %q", l)
		}
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
}

func TestDeleteAsksFirst(t *testing.T) {
	m := newTest(t)
	screen(t, m, 120, 20, rowsMsg{ss: sample()})
	m.Update(press(" ")) // mark api-7f3a, move to web-12bc
	m.Update(press(" ")) // mark web-12bc
	out := screen(t, m, 120, 20, press("d"))
	if m.mode != confirmMode || !strings.Contains(out, "Delete 2 sandboxes?") || !strings.Contains(out, "api-7f3a") {
		t.Fatalf("no confirmation:\n%s", out)
	}
	m.Update(press("n"))
	if m.mode != listMode || m.busy != "" || !strings.Contains(m.status, "nothing deleted") {
		t.Errorf("n didn't keep them: mode %v busy %q status %q", m.mode, m.busy, m.status)
	}
}

func TestFilter(t *testing.T) {
	m := newTest(t)
	screen(t, m, 120, 20, rowsMsg{ss: sample()})
	m.Update(press("/"))
	for _, r := range "web" {
		m.Update(press(string(r)))
	}
	out := screen(t, m, 120, 20)
	if len(m.rows) != 1 || m.rows[0].Name != "web-12bc" || !strings.Contains(out, `1 match "web"`) {
		t.Errorf("filter: %d rows\n%s", len(m.rows), out)
	}
	// An applied filter doesn't hide what's said on the status line.
	m.Update(press("enter"))
	m.say(good, "deleted web-12bc")
	if out := screen(t, m, 120, 20); !strings.Contains(out, "✓ deleted web-12bc") || !strings.Contains(out, `1 match "web"`) {
		t.Errorf("the message is hidden by the applied filter:\n%s", out)
	}
	m.Update(press("esc"))
	if len(m.rows) != 3 {
		t.Errorf("esc didn't clear the filter: %d rows", len(m.rows))
	}
}

func TestKeyAndProgressViews(t *testing.T) {
	m := newTest(t)
	screen(t, m, 100, 24, rowsMsg{ss: sample()})
	m.mode, m.busy = progressMode, "Creating 'api-7f3a'" // watched to the end
	m.Update(createdMsg{c: &core.Created{Name: "api-7f3a", NewKey: &core.NewKey{Sandbox: "api-7f3a",
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample opencode-sandbox-api-7f3a-2026-11-27", Fingerprint: "SHA256:abc",
		Inside: true, Why: "Apple container runs each sandbox in a VM of its own"}}})
	out := screen(t, m, 100, 24)
	for _, want := range []string{"New disposable SSH key for 'api-7f3a'", "INSIDE the sandbox", "ssh-ed25519 AAAA", "SHA256:abc", "y copy", "enter open the sandbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("key view: missing %q\n%s", want, out)
		}
	}
	m.Update(press("esc"))
	m.mode, m.busy = progressMode, "Creating 'x'"
	m.prog = progress{title: "Creating sandbox 'x' from ~/src/x", done: []string{"Building the sandbox image"}, current: "Creating sandbox 'x'",
		log: []string{"#1 building", "#2 done"}, notes: []string{"› using the only provider in your opencode config: p1"}}
	out = screen(t, m, 100, 24)
	for _, want := range []string{"Creating sandbox 'x' from ~/src/x", "✓ Building the sandbox image", "Creating sandbox 'x'", "#2 done", "using the only provider", "can't be cancelled"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress view: missing %q\n%s", want, out)
		}
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
}

// ctrlC is the ctrl+c key.
var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// quits reports whether cmd quits the dashboard.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// pump runs cmd and feeds the model what it produces within 100ms, a few
// rounds deep: enough for a form to settle, without waiting on timers.
func pump(m *model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth == 0 {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		// A batch or a sequence (an unexported slice of commands) is run
		// command by command; anything else goes to the model.
		if v := reflect.ValueOf(msg); msg != nil && v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := 0; i < v.Len(); i++ {
				pump(m, v.Index(i).Interface().(tea.Cmd), depth)
			}
		} else if msg != nil {
			_, next := m.Update(msg)
			pump(m, next, depth-1)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

// withForm opens the new-sandbox form on a config with two providers.
func withForm(t *testing.T) *model {
	t.Helper()
	m := newTest(t)
	cfg := filepath.Join(t.TempDir(), "oc.json")
	if err := os.WriteFile(cfg, []byte(`{"provider":{"p1":{"name":"One","models":{"m1":{}}},"p2":{"name":"Two","models":{"m2":{},"m3":{}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_SANDBOX_CONFIG", cfg)
	screen(t, m, 110, 32, rowsMsg{ss: sample()})
	_, cmd := m.Update(press("n"))
	pump(m, cmd, 4)
	if m.mode != formMode {
		t.Fatalf("n didn't open the form: mode %v, status %q", m.mode, m.status)
	}
	return m
}

func TestFormShowsEverythingAndItsKeys(t *testing.T) {
	m := withForm(t)
	out := screen(t, m, 110, 32)
	for _, want := range []string{"New sandbox", "Folder to copy", "Name", "SSH key", "new disposable key", "none", "Providers", "p1", "p2",
		"tab complete", "enter next", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("form: missing %q", want)
		}
	}
	if strings.Count(out, "enter next") != 1 {
		t.Errorf("the keys are shown %d times, want once", strings.Count(out, "enter next"))
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
}

func TestEscAndCtrlCLeaveTheForm(t *testing.T) {
	for name, key := range map[string]tea.KeyPressMsg{"esc": press("esc"), "ctrl+c": ctrlC} {
		m := withForm(t)
		_, cmd := m.Update(key)
		if m.mode != listMode || m.form != nil || m.busy != "" {
			t.Errorf("%s: still in the form (mode %v, busy %q)", name, m.mode, m.busy)
		}
		if quits(cmd) {
			t.Errorf("%s in the form quit the dashboard", name)
		}
		if out := screen(t, m, 110, 32); !strings.Contains(out, "Sandboxes (3)") || !strings.Contains(out, "no sandbox created") {
			t.Errorf("%s: not back at the list:\n%s", name, out)
		}
	}
	// After moving on a field, shift+tab goes back one and esc still leaves.
	// tab moves on from the name, which has nothing to complete.
	m := withForm(t)
	_, cmd := m.Update(press("enter"))
	pump(m, cmd, 4)
	if out := screen(t, m, 110, 32); !strings.Contains(out, "shift+tab previous field") || strings.Contains(out, "tab complete") {
		t.Errorf("on the name: no way back shown, or completion offered:\n%s", out)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	pump(m, cmd, 4)
	if out := screen(t, m, 110, 32); !strings.Contains(out, "↑ up · ↓ down") || !strings.Contains(out, "enter select") {
		t.Errorf("tab didn't move on from the name to the key list:\n%s", out)
	}
	// On the last field, enter creates; while filtering it, esc ends the
	// filter first and only then leaves the form.
	_, cmd = m.Update(press("enter"))
	pump(m, cmd, 4)
	if out := screen(t, m, 110, 32); !strings.Contains(out, "enter create") || !strings.Contains(out, "esc cancel") {
		t.Errorf("on the last field:\n%s", out)
	}
	// tab there doesn't create the sandbox.
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	pump(m, cmd, 4)
	if m.mode != formMode || m.busy != "" {
		t.Fatalf("tab on the last field left the form: mode %v, busy %q", m.mode, m.busy)
	}
	_, cmd = m.Update(press("/"))
	pump(m, cmd, 4)
	m.Update(press("esc"))
	if m.mode != formMode {
		t.Error("esc while filtering left the form instead of ending the filter")
	}
	m.Update(press("esc"))
	if m.mode != listMode {
		t.Error("esc on the second field didn't leave the form")
	}
}

func TestCtrlCQuitsOnlyFromTheList(t *testing.T) {
	m := newTest(t)
	screen(t, m, 120, 20, rowsMsg{ss: sample()})
	// Twice, so one ctrl+c too many after closing opencode doesn't quit.
	if _, cmd := m.Update(ctrlC); quits(cmd) || !strings.Contains(m.status, "again to quit") {
		t.Errorf("the first ctrl+c in the list: quit %v, status %q", quits(cmd), m.status)
	}
	if _, cmd := m.Update(ctrlC); !quits(cmd) {
		t.Error("the second ctrl+c in the list didn't quit")
	}
	m.Update(ctrlC)
	m.Update(press("j")) // another key in between starts over
	if _, cmd := m.Update(ctrlC); quits(cmd) {
		t.Error("ctrl+c quit after another key in between")
	}
	if _, cmd := m.Update(press("q")); !quits(cmd) {
		t.Error("q didn't quit")
	}
	for _, md := range []mode{keyMode, confirmMode, inputMode, healthMode} {
		m := newTest(t)
		screen(t, m, 120, 20, rowsMsg{ss: sample()})
		m.mode = md
		_, cmd := m.Update(ctrlC)
		if quits(cmd) || m.mode != listMode {
			t.Errorf("ctrl+c in mode %v: quit %v, mode %v; want back at the list", md, quits(cmd), m.mode)
		}
	}
}

func TestLeavingAndReturningToProgress(t *testing.T) {
	m := newTest(t)
	screen(t, m, 120, 20, rowsMsg{ss: sample()})
	m.mode, m.busy = progressMode, "Creating 'new-1'"
	m.prog = progress{title: "Creating sandbox 'new-1'", current: "Building the sandbox image"}

	out := screen(t, m, 120, 20, press("esc"))
	if m.mode != listMode || m.busy == "" || !strings.Contains(out, "Creating 'new-1'…") || !strings.Contains(out, "Sandboxes (3)") {
		t.Fatalf("esc didn't go back to the list with the work carrying on:\n%s", out)
	}
	// Nothing else may start meanwhile, and the dashboard can't be quit.
	for _, k := range []string{"n", "d", "enter", "s", "c", "b", "q"} {
		_, cmd := m.Update(press(k))
		if m.mode != listMode || quits(cmd) || !strings.Contains(m.status, "wait until it's done creating 'new-1'") {
			t.Errorf("%s while busy: mode %v, quit %v, status %q", k, m.mode, quits(cmd), m.status)
		}
	}
	if out := screen(t, m, 120, 20); !strings.Contains(out, "wait until it's done") {
		t.Errorf("the reason isn't shown while busy:\n%s", out)
	}

	// Done while unwatched: said and pointed at, not opened.
	created := core.Sandbox{Name: "new-1", State: core.StateNew, Status: "new", RunsIn: "apple", Engine: "apple", KeyKind: "none", KeyName: "none"}
	_, cmd := m.Update(createdMsg{c: &core.Created{Name: "new-1"}})
	if m.busy != "" || m.mode != listMode || !strings.Contains(m.status, "created 'new-1'; enter opens it") {
		t.Errorf("after creation: busy %q, mode %v, status %q", m.busy, m.mode, m.status)
	}
	_ = cmd // a reload; here the list comes from the test
	m.Update(rowsMsg{ss: append(sample(), created)})
	if cur := m.current(); cur == nil || cur.Name != "new-1" {
		t.Errorf("the new sandbox isn't selected: %+v", cur)
	}

	// A failure while unwatched is shown.
	m.busy, m.prog = "Creating 'new-2'", progress{title: "Creating sandbox 'new-2'", log: []string{"#3 ERROR: no space left"}}
	m.Update(createdMsg{err: errors.New("could not build the image")})
	out = screen(t, m, 120, 20)
	if m.mode != progressMode || !strings.Contains(out, "could not build the image") || !strings.Contains(out, "no space left") {
		t.Errorf("failure not shown:\n%s", out)
	}
	m.Update(press("esc"))
	if m.mode != listMode {
		t.Error("esc didn't leave the failure")
	}
}

func TestArrowsMoveWhileFiltering(t *testing.T) {
	m := newTest(t)
	screen(t, m, 120, 20, rowsMsg{ss: sample()})
	m.Update(press("/"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.filtering || m.cursor != 1 {
		t.Errorf("down while filtering: filtering %v, cursor %d", m.filtering, m.cursor)
	}
}
