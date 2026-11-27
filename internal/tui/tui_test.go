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
			KeyKind: "disposable", KeyName: "disposable", KeyInside: true, Providers: "my-provider", Gateway: true, Source: "/src/api", Version: core.Version},
		{Name: "web-12bc", State: core.StateClosed, Status: "closed 2 hours ago", Created: "3 days ago", Engine: "docker", RunsIn: "gvisor",
			KeyKind: "home", Key: "/h/.ssh/id_ed25519", KeyName: "id_ed25519", Providers: "anthropic,openai", APIKeysInside: true, Source: "/src/web", Version: "0.4.0"},
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
	for _, want := range []string{"a VM of its own (Apple container)", "disposable, stored inside the sandbox", "s shell · open in another terminal",
		"api keys on this machine, behind a gateway", "gateway not running; reopen to restart it"} {
		if !strings.Contains(details, want) {
			t.Errorf("details: missing %q in %q", want, details)
		}
	}
	// Where a sandbox's API keys are, in the list and in words.
	ss := sample()
	for i, want := range []struct{ listed, words string }{
		{"my-provider", "on this machine, behind a gateway"},
		{"anthropic,openai*", "inside the sandbox (it has no gateway)"},
		{"all (old sandbox)", "none"},
	} {
		if got := ui.Providers(ss[i]); got != want.listed {
			t.Errorf("%s is listed with %q, want %q", ss[i].Name, got, want.listed)
		}
		if got := ui.APIKeys(ss[i]); got != want.words {
			t.Errorf("%s: API keys %q, want %q", ss[i].Name, got, want.words)
		}
	}
	ss[0].APIKeysInside = true
	if got := ui.APIKeys(ss[0]); !strings.Contains(got, "inside the sandbox for a provider") || !strings.Contains(got, "the rest on this machine") {
		t.Errorf("a gateway that can't serve every provider: %q", got)
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

// The form says which dot files of the folder typed are copied too.
func TestFormNamesDotFiles(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	for _, f := range []string{".env", ".npmrc", ".gitignore"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	m := withForm(t)
	if got, want := m.nf.about(m.c), "copied into /workspace, never mounted\nits dot files too, which often hold secrets: .env · .npmrc"; got != want {
		t.Errorf("about the folder: %q, want %q", got, want)
	}
	raw := screen(t, m, 110, 32)
	out := strings.Join(strings.Fields(strings.ReplaceAll(raw, "┃", " ")), " ")
	if !strings.Contains(out, "never mounted its dot files too, which often hold secrets: .env · .npmrc >") || strings.Contains(out, ".gitignore") {
		t.Errorf("the form doesn't name them:\n%s", raw)
	}
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + raw)
	}
	// A folder without any: only what happens to it.
	clean := filepath.Join(dir, "src")
	if err := os.Mkdir(clean, 0o755); err != nil {
		t.Fatal(err)
	}
	m.nf.folder = clean
	if got := m.nf.about(m.c); got != "copied into /workspace, never mounted" {
		t.Errorf("a folder without dot files: %q", got)
	}
	m.nf.folder = filepath.Join(dir, "nope")
	if got := m.nf.about(m.c); got != "copied into /workspace, never mounted" {
		t.Errorf("a folder that isn't there: %q", got)
	}
}

// A folder typed with ~ is completed as typed. The form opens on "~" when
// the dashboard is started in the home folder.
func TestCompletingUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"Github/api", "Github/web", "Documents", ".ssh"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		typed string
		want  []string
	}{
		{"~", []string{"~/"}},
		{"~/", []string{"~/Documents/", "~/Github/"}},
		{"~/G", []string{"~/Github/"}},
		{"~/Github/", []string{"~/Github/api/", "~/Github/web/"}},
		{"~/Github/a", []string{"~/Github/api/"}},
		{"~/.", []string{"~/.ssh/"}},
		{"~/x", nil},
		{home + "/G", []string{home + "/Github/"}},
		{"", nil},
	} {
		if got := dirSuggestions(c.typed); !reflect.DeepEqual(got, c.want) {
			t.Errorf("suggestions for %q: %q, want %q", c.typed, got, c.want)
		}
	}
	for _, c := range [][2]string{
		{"~", "~/"},
		{"~/", "~/"},
		{"~/G", "~/Github/"},
		{"~/Github/", "~/Github/"},
		{"~/Github/a", "~/Github/api/"},
		{"~/n", "~/notes.txt"},
		{"~/x", "~/x"},
		{home + "/D", home + "/Documents/"},
	} {
		if got := complete(c[0]); got != c[1] {
			t.Errorf("completing %q: %q, want %q", c[0], got, c[1])
		}
	}
}

// ctrl+o adds the options that have defaults to the form, after its last
// field; without it, they aren't asked and a sandbox gets the defaults.
func TestFormMoreOptions(t *testing.T) {
	m := withForm(t)
	out := screen(t, m, 110, 32)
	if !strings.Contains(out, "ctrl+o more options") || strings.Contains(out, "Runs in") {
		t.Errorf("the form before ctrl+o:\n%s", out)
	}
	if o := m.nf.options("x"); o.Network != "open" || o.Gateway != "on" || o.Engine != "" || o.Runtime != "" || o.Key != "new" {
		t.Errorf("what's asked for without more options: %+v", o)
	}
	enter := press("enter")
	walk := func(keys ...tea.KeyPressMsg) {
		t.Helper()
		for _, k := range keys {
			_, cmd := m.Update(k)
			pump(m, cmd, 6)
		}
	}
	walk(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	out = screen(t, m, 110, 32)
	if !m.nf.more || strings.Contains(out, "ctrl+o more options") || !strings.Contains(out, "more options follow the last field") {
		t.Fatalf("after ctrl+o: more %v\n%s", m.nf.more, out)
	}
	// Through the first page: folder, name, key, a provider.
	walk(enter, enter, enter, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, enter)
	out = screen(t, m, 110, 32)
	for _, want := range []string{"New sandbox: more options", "Network", "open", "none", "only its providers, through the gateway", "Runs in", "API keys", "on this machine", "inside the sandbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("the second page lacks %q:\n%s", want, out)
		}
	}
	if m.mode != formMode {
		t.Fatalf("the form ended before its second page: mode %v", m.mode)
	}
	// No network: no key, whatever was picked before; and the gateway.
	walk(press("j"), enter)
	if m.nf.network != "none" {
		t.Fatalf("network picked: %q", m.nf.network)
	}
	if o := m.nf.options("x"); o.Network != "none" || o.Key != "none" || len(o.Providers) != 1 {
		t.Errorf("what's asked for without a network: %+v", o)
	}
	m.nf.gateway = "off"
	if o := m.nf.options("x"); o.Gateway != "off" {
		t.Errorf("gateway off: %+v", o)
	}
	// Where it runs, as the form holds it.
	for v, want := range map[string][2]string{"|": {"", ""}, "apple|": {"apple", ""}, "docker|runsc-ssh": {"docker", "runsc-ssh"}} {
		if e, rt := placeOf(v); e != want[0] || rt != want[1] {
			t.Errorf("placeOf(%q) = %q, %q", v, e, rt)
		}
	}
}

// Where new sandboxes get no network by default, the form says so, and
// asks for none without more being asked.
func TestFormWithoutNetworkByDefault(t *testing.T) {
	t.Setenv(core.NetworkEnv, "none")
	m := withForm(t)
	if out := screen(t, m, 110, 32); !strings.Contains(out, "new sandboxes get no network (OPENCODE_SANDBOX_NETWORK)") {
		t.Errorf("the form doesn't say so:\n%s", out)
	}
	if o := m.nf.options("x"); o.Network != "none" || o.Key != "none" {
		t.Errorf("what's asked for: %+v", o)
	}
}
