package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// netView is the view of a sandbox with a filtered network: the hosts it
// may reach, and the ones it was refused, which can be allowed or denied
// from here.
type netView struct {
	sandbox string
	n       core.Net
	cursor  int // among the refused hosts
}

// startNet opens the view for the sandbox under the cursor.
func (m *model) startNet() tea.Cmd {
	s := m.current()
	switch {
	case s == nil:
		return nil
	case !s.Filtered:
		m.say(info, "'%s' has no list of hosts: its network is %s. A filtered network is chosen when a sandbox is made (n, then ctrl+o)", s.Name, ui.Network(*s))
		return nil
	}
	m.net = netView{sandbox: s.Name}
	m.loadNet()
	m.mode = netMode
	return nil
}

// loadNet reads the sandbox's rules and refused hosts again: its gateway
// writes them as it goes.
func (m *model) loadNet() {
	n, err := core.ReadNet(m.net.sandbox)
	if err != nil {
		m.say(bad, "%v", err)
		return
	}
	// The cursor stays on the host it was on.
	on := ""
	if m.net.cursor < len(m.net.n.Blocked) {
		b := m.net.n.Blocked[m.net.cursor]
		on = ui.HostPort(b.Host, b.Port)
	}
	m.net.n = n
	for i, b := range n.Blocked {
		if ui.HostPort(b.Host, b.Port) == on {
			m.net.cursor = i
		}
	}
	m.net.cursor = min(m.net.cursor, max(len(n.Blocked)-1, 0))
}

func (m *model) netKey(s string) (tea.Model, tea.Cmd) {
	v := &m.net
	switch s {
	case "esc", "q":
		m.mode = listMode
	case "up", "k":
		v.cursor = max(v.cursor-1, 0)
	case "down", "j":
		v.cursor = min(v.cursor+1, max(len(v.n.Blocked)-1, 0))
	case "r":
		m.loadNet()
	case "a", "enter", "d":
		if v.cursor >= len(v.n.Blocked) {
			m.say(info, "nothing was refused: there's no host here to allow or deny")
			return m, nil
		}
		b := v.n.Blocked[v.cursor]
		host, name, c := ui.HostPort(b.Host, b.Port), v.sandbox, m.c
		if s == "d" {
			return m, m.background("Denying "+host, func() tea.Msg {
				return opMsg{ok: fmt.Sprintf("'%s' is denied %s", name, host), err: c.Deny(name, host)}
			})
		}
		return m, m.background("Allowing "+host, func() tea.Msg {
			return opMsg{ok: fmt.Sprintf("'%s' may reach %s", name, ui.NetRule(host)), err: c.Allow(name, host)}
		})
	}
	return m, nil
}

// netBody draws the view in w columns and h lines.
func (m *model) netBody(w, h int) string {
	t, v := m.t, m.net
	var b strings.Builder
	head := func(s string) { b.WriteString(t.Bold.Render(s) + "\n") }
	head("May reach, besides its providers")
	if len(v.n.Policy.Presets)+len(v.n.Policy.Allow) == 0 {
		b.WriteString(t.Dim.Render("  nothing yet") + "\n")
	}
	for _, p := range v.n.Policy.Presets {
		b.WriteString("  " + trunc(ui.NetWord(p), w-2) + "\n")
	}
	for _, r := range v.n.Policy.Allow {
		b.WriteString("  " + trunc(ui.NetRule(r), w-2) + "\n")
	}
	if len(v.n.Policy.Deny) > 0 {
		b.WriteString("\n")
		head("Denied, whatever else allows it")
		for _, r := range v.n.Policy.Deny {
			b.WriteString("  " + trunc(ui.NetRule(r), w-2) + "\n")
		}
	}
	b.WriteString("\n")
	head("Refused")
	if len(v.n.Blocked) == 0 {
		b.WriteString(t.Dim.Render("  nothing: what the sandbox tries to reach and may not shows here") + "\n")
	}
	used := strings.Count(b.String(), "\n")
	// As many refused hosts as fit, around the cursor.
	room := max(h-used, 1)
	from := min(max(v.cursor-room/2, 0), max(len(v.n.Blocked)-room, 0))
	hostW := 0
	for _, r := range v.n.Blocked {
		hostW = max(hostW, len(ui.HostPort(r.Host, r.Port)))
	}
	hostW = min(hostW, max(w/2, 10))
	for i := from; i < len(v.n.Blocked) && i < from+room; i++ {
		r := v.n.Blocked[i]
		mark, host := "  ", pad(ui.HostPort(r.Host, r.Port), hostW)
		if i == v.cursor {
			mark, host = t.Accented.Render("▸ "), t.Bold.Render(host)
		}
		b.WriteString(mark + host + "  " + t.Dim.Render(trunc(ui.Refused(r), w-hostW-4)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
