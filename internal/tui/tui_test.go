package tui

import (
	"os"
	"strings"
	"testing"

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
	m.Update(press("esc"))
	if len(m.rows) != 3 {
		t.Errorf("esc didn't clear the filter: %d rows", len(m.rows))
	}
}

func TestKeyAndProgressViews(t *testing.T) {
	m := newTest(t)
	screen(t, m, 100, 24, rowsMsg{ss: sample()})
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
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !strings.Contains(m.status, "wait until") {
		t.Errorf("ctrl+c while busy: status %q", m.status)
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
}
