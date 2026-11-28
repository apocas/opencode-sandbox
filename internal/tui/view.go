package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "opencode-sandbox"
	return v
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

func pad(s string, w int) string {
	s = trunc(s, w)
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

func (m *model) render() string {
	if m.w == 0 {
		return ""
	}
	if m.intro {
		return m.introView(m.introFrame, m.w, m.h)
	}
	header := m.header()
	footer := m.footer()
	status := m.statusLine()
	bodyH := max(m.h-lipgloss.Height(header)-lipgloss.Height(footer)-lipgloss.Height(status), 3)
	var body string
	switch m.mode {
	case healthMode:
		body = m.panel("Health", m.vp.View(), m.w, bodyH, true)
	case formMode:
		body = lipgloss.Place(m.w, bodyH, lipgloss.Center, lipgloss.Top,
			lipgloss.NewStyle().Padding(1, 2).Render(m.form.View()))
	case progressMode:
		body = m.center(m.progressBox(), bodyH)
	case keyMode:
		body = m.center(m.keyBox(), bodyH)
	case confirmMode:
		body = m.center(m.confirmBox(), bodyH)
	default:
		body = m.main(bodyH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status, footer)
}

func (m *model) center(box string, h int) string {
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m *model) header() string {
	t := m.t
	left := " " + t.Title.Render("opencode-sandbox") + " " + t.Dim.Render(core.Version)
	var es []string
	for _, e := range m.c.Engines() {
		s := e.Name + " " + e.Version
		if e.With != "" {
			s += " with " + e.With
		}
		if e.New {
			s = t.Accented.Render("new → ") + s
		}
		es = append(es, s)
	}
	if len(es) > 0 {
		left += t.Dim.Render("   ") + strings.Join(es, t.Dim.Render(" · "))
	}
	right := t.Dim.Render("? help ")
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return trunc(left, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// statusLine is the line above the key hints: a prompt while one is open,
// else what's running in the background and the last thing that was said.
func (m *model) statusLine() string {
	t := m.t
	switch {
	case m.mode == inputMode:
		return " " + m.in.ti.View()
	case m.filtering:
		// Only while typing in it: an applied filter is named in the list's
		// title, and mustn't hide what's said here.
		return " " + m.filter.View()
	}
	line := ""
	if m.busy != "" {
		line = m.spin.View() + " " + m.busy + "…"
	}
	if m.status != "" {
		if line != "" {
			line += t.Dim.Render("  ·  ")
		}
		switch m.statusKind {
		case good:
			line += t.Success(m.status)
		case warn:
			line += t.Warning(m.status)
		case bad:
			line += t.Failure(m.status)
		default:
			line += t.Info(m.status)
		}
	}
	return " " + trunc(line, m.w-2)
}

func (m *model) footer() string {
	t := m.t
	k := func(key, what string) string { return t.Key.Render(key) + " " + t.Dim.Render(what) }
	var items []string
	switch m.mode {
	case healthMode:
		items = []string{k("↑/↓", "scroll"), k("esc", "back")}
		if m.setupErr != nil {
			items = []string{k("↑/↓", "scroll"), k("q", "quit")}
		}
	case formMode:
		for _, h := range formHints(m.form) {
			items = append(items, k(h[0], h[1]))
		}
		if m.nf != nil && !m.nf.more {
			items = append(items, k("ctrl+o", "more options"))
		}
	case progressMode:
		if m.prog.finished {
			items = []string{k("esc", "back")}
		} else {
			items = []string{k("esc", "back to the list"), t.Dim.Render("it carries on, and can't be cancelled halfway")}
		}
	case keyMode:
		items = []string{k("y", "copy")}
		if m.key.openAfter != "" {
			items = append(items, k("enter", "open the sandbox"), k("esc", "back to the list"))
		} else {
			items = append(items, k("esc", "close"))
		}
	case confirmMode:
		items = []string{k("y", "delete"), k("n", "keep")}
	case inputMode:
		items = []string{k("enter", "copy"), k("esc", "cancel")}
		if m.in.op == "cp" {
			items = append(items, k("tab", "complete"))
		}
	default:
		if m.filtering {
			items = []string{k("enter", "keep filter"), k("esc", "clear")}
			break
		}
		items = []string{k("enter", "open"), k("n", "new"), k("s", "shell"), k("c", "copy in"), k("g", "get"),
			k("p", "key"), k("d", "delete"), k("/", "filter"), k("h", "health"), k("?", "more"), k("q", "quit")}
		if m.showHelp {
			items = append(items[:len(items)-2], k("space", "mark"), k("esc", "unmark"), k("b", "rebuild image"),
				k("r", "refresh"), k("j/k", "move"), k("?", "less"), k("q", "quit"))
		}
	}
	// Wrap the hints to the width.
	var lines []string
	line := " "
	for _, it := range items {
		if lipgloss.Width(line)+lipgloss.Width(it)+3 > m.w && line != " " {
			lines = append(lines, line)
			line = " "
		}
		if line != " " {
			line += t.Dim.Render(" · ")
		}
		line += it
	}
	return strings.Join(append(lines, line), "\n")
}

// panel is a bordered box of exactly w×h with a title.
func (m *model) panel(title, content string, w, h int, focus bool) string {
	border := m.t.Line
	if focus {
		border = m.t.Accent
	}
	inner := lipgloss.NewStyle().Width(w - 4).Height(h - 3).MaxHeight(h - 3).Render(content)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Width(w).Height(h).
		Render(m.t.Bold.Render(title) + "\n" + inner)
}

// main is the list, and the details of the current sandbox beside it where
// there's room.
func (m *model) main(h int) string {
	listW := m.w
	showDetails := m.w >= 96
	if showDetails {
		listW = m.w * 3 / 5
	}
	title := "Sandboxes"
	if m.loaded {
		title += " " + m.t.Dim.Render(fmt.Sprintf("(%d)", len(m.all)))
	}
	if q := m.filter.Value(); q != "" {
		title += m.t.Dim.Render(fmt.Sprintf(" · %d match %q", len(m.rows), q))
	}
	if n := len(m.marked); n > 0 {
		title += m.t.Accented.Render(fmt.Sprintf(" · %d marked", n))
	}
	list := m.panel(title, m.list(listW-4, h-3), listW, h, true)
	if !showDetails {
		return list
	}
	dt := "Details"
	if s := m.current(); s != nil {
		dt = s.Name
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, m.panel(dt, m.details(m.w-listW-4), m.w-listW, h, false))
}

func (m *model) list(w, h int) string {
	t := m.t
	if !m.loaded {
		return t.Dim.Render("looking for sandboxes…")
	}
	if len(m.all) == 0 {
		return "\n" + t.Bold.Render("No sandboxes yet.") + "\n" + t.Dim.Render("Press ") + t.Key.Render("n") +
			t.Dim.Render(" to create one with a copy of a folder.")
	}
	if len(m.rows) == 0 {
		return t.Dim.Render("nothing matches the filter (esc clears it)")
	}
	nameW := 4
	for _, s := range m.rows {
		nameW = max(nameW, lipgloss.Width(s.Name))
	}
	nameW = min(nameW, 28)
	const stateW, runsW = 19, 6
	provW := w - 6 - nameW - 1 - stateW - 1 - runsW - 1
	off := 0
	if m.cursor >= h {
		off = m.cursor - h + 1
	}
	var out []string
	for i := off; i < len(m.rows) && i < off+h; i++ {
		s := m.rows[i]
		mark := " "
		if m.marked[s.Name] {
			mark = "✓"
		}
		cols := pad(s.Name, nameW) + " " + pad(ui.Status(s), stateW) + " " + pad(s.RunsIn, runsW)
		if provW > 4 {
			cols += " " + pad(ui.Providers(s), provW)
		}
		if i == m.cursor {
			row := "› " + mark + " " + plainBadge(s) + " " + cols
			out = append(out, lipgloss.NewStyle().Background(t.Sel).Foreground(t.Accent).Bold(true).Render(pad(row, w)))
			continue
		}
		row := "  " + t.Accented.Render(mark) + " " + t.Badge(s) + " " + pad(s.Name, nameW) + " " +
			pad(t.StatusStyled(s), stateW) + " " + t.Dim.Render(pad(s.RunsIn, runsW))
		if provW > 4 {
			row += " " + t.Dim.Render(pad(ui.Providers(s), provW))
		}
		out = append(out, row)
	}
	return strings.Join(out, "\n")
}

func plainBadge(s core.Sandbox) string {
	switch s.State {
	case core.StateOpen:
		return "●"
	case core.StateClosed:
		return "○"
	case core.StateNew:
		return "◌"
	}
	return "?"
}

func (m *model) details(w int) string {
	t := m.t
	s := m.current()
	if s == nil {
		return t.Dim.Render("Nothing selected.")
	}
	const labelW = 13
	var b strings.Builder
	row := func(label, value string) {
		lines := strings.Split(lipgloss.Wrap(value, max(w-labelW, 10), " /"), "\n")
		for i, l := range lines {
			if i == 0 {
				b.WriteString(t.Dim.Render(pad(label, labelW)) + l + "\n")
			} else {
				b.WriteString(strings.Repeat(" ", labelW) + l + "\n")
			}
		}
	}
	row("state", t.StatusStyled(*s))
	row("runs in", ui.RunsIn(*s))
	row("network", ui.NetworkWords(*s))
	row("limits", ui.Limits(*s))
	row("copied from", ui.Source(*s))
	provs := s.Providers
	if provs == "" {
		provs = ui.Providers(*s)
	}
	row("providers", provs)
	row("api keys", ui.APIKeys(*s))
	if s.Gateway && s.State == core.StateOpen {
		if m.gateways[s.Name] {
			row("gateway", t.Fg(t.OK).Render("running")+t.Dim.Render(", putting the keys into its requests"))
		} else {
			row("gateway", t.Fg(t.Warn).Render("not running")+t.Dim.Render("; reopen to restart it"))
		}
	}
	switch {
	case s.KeyKind == "none":
		row("ssh key", "none")
	default:
		k := s.KeyName
		switch {
		case s.KeyInside:
			k += ", stored inside the sandbox"
		case s.KeyKind == "disposable":
			k += ", on this machine"
		default:
			k += " from ~/.ssh, on this machine"
		}
		row("ssh key", k)
		if fp := m.fps[s.Name]; fp != "" {
			row("", t.Dim.Render(fp))
		}
		if !s.KeyInside && s.State == core.StateOpen {
			if m.agents[s.Name] {
				row("agent", t.Fg(t.OK).Render("running")+t.Dim.Render(", holding only this key"))
			} else {
				row("agent", t.Fg(t.Warn).Render("not running")+t.Dim.Render("; reopen to restart it"))
			}
		}
	}
	row("created", s.Created)
	v := s.Version
	switch {
	case v == "":
		row("made by", t.Fg(t.Warn).Render("opencode-sandbox before 0.1"))
	case v != core.Version:
		row("made by", "opencode-sandbox "+v+t.Dim.Render(" (keeps its original image)"))
	default:
		row("made by", "opencode-sandbox "+v)
	}
	b.WriteString("\n")
	k := func(key, what string) string { return t.Key.Render(key) + " " + t.Dim.Render(what) }
	switch {
	case s.State == core.StateOpen && s.Engine == "apple":
		b.WriteString(k("s", "shell") + t.Dim.Render(" · open in another terminal"))
	case s.State == core.StateOpen:
		b.WriteString(k("enter", "attach") + t.Dim.Render(" · ") + k("s", "shell"))
	default:
		b.WriteString(k("enter", "open") + t.Dim.Render(" · continues its last session"))
	}
	return b.String()
}

func (m *model) box(content string) string {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.t.Accent).
		Padding(1, 2).Width(min(m.w-4, 84)).Render(content)
}

func (m *model) boxWidth() int { return min(m.w-4, 84) - 6 }

func (m *model) progressBox() string {
	t, p := m.t, m.prog
	w := m.boxWidth()
	var b strings.Builder
	b.WriteString(t.Bold.Render(trunc(p.title, w)) + "\n\n")
	for _, s := range p.done {
		b.WriteString(t.Success(trunc(s, w-2)) + "\n")
	}
	if p.current != "" {
		b.WriteString(m.spin.View() + " " + trunc(p.current, w-2) + "\n")
	}
	for _, n := range p.notes {
		b.WriteString(t.Dim.Render(trunc(n, w)) + "\n")
	}
	if len(p.log) > 0 {
		tail := p.log[max(len(p.log)-8, 0):]
		for i, l := range tail {
			tail[i] = trunc(l, w-2)
		}
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(t.Muted).BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(t.Line).PaddingLeft(1).Render(strings.Join(tail, "\n")) + "\n")
	}
	if p.err != nil {
		b.WriteString("\n" + t.Failure(lipgloss.Wrap(p.err.Error(), w-2, " ")) + "\n")
	}
	return m.box(strings.TrimRight(b.String(), "\n"))
}

func (m *model) keyBox() string {
	t, k := m.t, m.key
	w := m.boxWidth()
	body := t.Bold.Render(k.title) + "\n\n" + lipgloss.Wrap(k.body, w, " ") + "\n\n" +
		t.Accented.Render(lipgloss.Wrap(k.pub, w, "")) + "\n"
	if k.fp != "" {
		body += "\n" + t.Dim.Render(k.fp)
	}
	return m.box(body)
}

func (m *model) confirmBox() string {
	t := m.t
	w := m.boxWidth()
	title := fmt.Sprintf("Delete %d sandboxes?", len(m.del))
	if len(m.del) == 1 {
		title = "Delete '" + m.del[0] + "'?"
	}
	var b strings.Builder
	b.WriteString(t.Fg(t.Err).Bold(true).Render(title) + "\n\n")
	if len(m.del) > 1 {
		for _, n := range m.del {
			b.WriteString("  • " + n + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(lipgloss.Wrap("Its files go too: /workspace, opencode's sessions, and a disposable key. Wherever you authorized that key, remove it from authorized_keys.", w, " "))
	return m.box(b.String())
}
