package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/netpolicy"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// The view of a sandbox with a filtered network: its list, what it's denied
// and what it was refused, with a key to allow or deny a refused host.
func TestNetView(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := netpolicy.Store{Dir: state.NetDir("filt-1")}
	for _, w := range []string{"github", "example.com:8443"} {
		if err := store.Update(func(p *netpolicy.Policy) error { return p.AllowWord(w) }); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Update(func(p *netpolicy.Policy) error { return p.DenyWord("x.example.com") })
	_ = store.Refused("models.opencode.ai", 443, "it's on no list")
	_ = store.Refused("models.opencode.ai", 443, "it's on no list")
	_ = store.Refused("db.internal", 5432, "it's on no list")

	m := newTest(t)
	ss := append(sample(), core.Sandbox{Name: "filt-1", State: core.StateOpen, Status: "open (1 minute)", Created: "1 minute ago",
		Engine: "docker", RunsIn: "gvisor", KeyKind: "none", KeyName: "none", Providers: "p1", Gateway: true, Filtered: true, Source: "/src/x", Version: core.Version})
	out := screen(t, m, 130, 30, rowsMsg{ss: ss})
	// On a sandbox whose network is open there's no list to show.
	m.Update(press("a"))
	if m.mode != listMode || !strings.Contains(m.status, "'api-7f3a' has no list of hosts: its network is open") {
		t.Errorf("a on a sandbox with an open network: mode %v, %q", m.mode, m.status)
	}
	for i, r := range m.rows {
		if r.Name == "filt-1" {
			m.cursor = i
		}
	}
	details := strings.Join(strings.Fields(ansi.Strip(m.details(60))), " ")
	for _, want := range []string{"network filtered: it reaches only its providers and the hosts on its list", "hosts 2 hosts on its list, 2 hosts refused · a shows them"} {
		if !strings.Contains(details, want) {
			t.Errorf("details: missing %q in %q", want, details)
		}
	}
	m.Update(press("a"))
	if m.mode != netMode {
		t.Fatalf("a on a sandbox with a filtered network: mode %v, %q", m.mode, m.status)
	}
	out = screen(t, m, 130, 30)
	for _, want := range []string{"Network of 'filt-1' (filtered)", "May reach, besides its providers",
		"github, a built-in list: git, the API and release downloads of github.com (11 hosts)", "example.com:8443",
		"Denied, whatever else allows it", "x.example.com (ports 443, 80 and 22)",
		"Refused", "▸ db.internal:5432", "once, just now: it's on no list", "models.opencode.ai", "2 times, just now: it's on no list",
		"a allow the host", "d deny it", "esc back"} {
		if !strings.Contains(out, want) {
			t.Errorf("the view lacks %q:\n%s", want, out)
		}
	}
	// The key acts on the host under the cursor.
	m.Update(press("j"))
	m.Update(press("j")) // no further than the last
	if m.net.cursor != 1 {
		t.Errorf("cursor at %d", m.net.cursor)
	}
	if _, cmd := m.Update(press("d")); cmd == nil || m.busy != "Denying models.opencode.ai" {
		t.Errorf("d: busy %q", m.busy)
	}
	m.busy = ""
	m.Update(press("k"))
	if _, cmd := m.Update(press("a")); cmd == nil || m.busy != "Allowing db.internal:5432" {
		t.Errorf("a: busy %q", m.busy)
	}
	m.busy = ""
	// What its gateway writes down since shows at the next refresh, with the
	// cursor still on its host.
	_ = store.Refused("new.example", 443, "it's on no list")
	m.Update(rowsMsg{ss: ss})
	if len(m.net.n.Blocked) != 3 || m.net.n.Blocked[m.net.cursor].Host != "db.internal" {
		t.Errorf("after a refresh: %d refused, cursor on %s", len(m.net.n.Blocked), m.net.n.Blocked[m.net.cursor].Host)
	}
	m.Update(press("esc"))
	if m.mode != listMode {
		t.Errorf("esc: mode %v", m.mode)
	}
	// With nothing refused, there's nothing to allow, and that's said.
	_ = store.Update(func(p *netpolicy.Policy) error {
		for _, w := range []string{"models.opencode.ai", "db.internal:5432", "new.example"} {
			if err := p.AllowWord(w); err != nil {
				return err
			}
		}
		return nil
	})
	p, _ := store.Load()
	_ = store.Forget(p.Rules())
	m.Update(press("a"))
	out = screen(t, m, 130, 30)
	if !strings.Contains(out, "nothing: what the sandbox tries to reach and may not shows here") {
		t.Errorf("with nothing refused:\n%s", out)
	}
	m.Update(press("a"))
	if m.busy != "" || !strings.Contains(m.status, "nothing was refused") {
		t.Errorf("a with nothing refused: busy %q, %q", m.busy, m.status)
	}
	// A sandbox that's deleted while its view is open: back to the list.
	m.Update(rowsMsg{ss: sample()})
	if m.mode != listMode {
		t.Errorf("after the sandbox was deleted: mode %v", m.mode)
	}
}
