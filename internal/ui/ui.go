// Package ui is how opencode-sandbox looks in a terminal: colors, badges,
// tables and the health report, shared by the command line and the
// dashboard. Colors adapt to light and dark terminals and disappear where
// the output isn't a terminal or NO_COLOR is set.
package ui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/state"
	"github.com/apocas/opencode-sandbox/internal/tty"
)

// Theme is the palette and styles, for a light or a dark terminal.
type Theme struct {
	Dark                                     bool
	Accent, OK, Warn, Err, Muted, Line, Text color.Color
	// Sel is the background of the selected row.
	Sel color.Color

	Title, Bold, Dim, Key, Accented lipgloss.Style
}

// NewTheme makes the theme for a dark or light background.
func NewTheme(dark bool) *Theme {
	ld := lipgloss.LightDark(dark)
	c := lipgloss.Color
	t := &Theme{
		Dark:   dark,
		Accent: ld(c("#6D28D9"), c("#A78BFA")),
		OK:     ld(c("#15803D"), c("#4ADE80")),
		Warn:   ld(c("#B45309"), c("#FBBF24")),
		Err:    ld(c("#B91C1C"), c("#F87171")),
		Muted:  ld(c("#6B7280"), c("#9CA3AF")),
		Line:   ld(c("#D1D5DB"), c("#3F3F46")),
		Text:   ld(c("#111827"), c("#E5E7EB")),
		Sel:    ld(c("#EDE9FE"), c("#2E1065")),
	}
	t.Title = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.Bold = lipgloss.NewStyle().Bold(true)
	t.Dim = lipgloss.NewStyle().Foreground(t.Muted)
	t.Key = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.Accented = lipgloss.NewStyle().Foreground(t.Accent)
	return t
}

// DetectTheme picks the theme for this terminal: dark, unless COLORFGBG
// (set by many terminals) says the background is light. It doesn't ask the
// terminal, which would take seconds where it doesn't answer and could eat
// keys typed meanwhile; the dashboard asks through Bubble Tea instead.
func DetectTheme() *Theme { return NewTheme(darkFromEnv(os.Getenv("COLORFGBG"))) }

// darkFromEnv reads COLORFGBG ("fg;bg", e.g. "0;15" for black on white).
func darkFromEnv(v string) bool {
	f := strings.Split(v, ";")
	switch f[len(f)-1] {
	case "7", "15":
		return false
	}
	return true
}

func (t *Theme) fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// Fg is a style with that foreground color.
func (t *Theme) Fg(c color.Color) lipgloss.Style { return t.fg(c) }

// Badge is a sandbox's state as a colored dot.
func (t *Theme) Badge(s core.Sandbox) string {
	switch s.State {
	case core.StateOpen:
		return t.fg(t.OK).Render("●")
	case core.StateClosed:
		return t.fg(t.Muted).Render("○")
	case core.StateNew:
		return t.fg(t.Accent).Render("◌")
	}
	return t.fg(t.Warn).Render("?")
}

// Status is a sandbox's state in words: "open · 5 minutes", "closed 2
// hours ago", "new".
func Status(s core.Sandbox) string {
	if v, ok := strings.CutPrefix(s.Status, "open ("); ok {
		return "open · " + strings.TrimSuffix(v, ")")
	}
	return s.Status
}

// StatusStyled is Status, colored by state.
func (t *Theme) StatusStyled(s core.Sandbox) string {
	switch s.State {
	case core.StateOpen:
		return t.fg(t.OK).Render(Status(s))
	case core.StateOther:
		return t.fg(t.Warn).Render(Status(s))
	}
	return t.Dim.Render(Status(s))
}

// KeyName is a sandbox's key as listed, with * when it's stored inside.
func KeyName(s core.Sandbox) string {
	if s.KeyInside {
		return s.KeyName + "*"
	}
	return s.KeyName
}

// Providers is a sandbox's providers as listed, with * when an API key of
// theirs is stored inside.
func Providers(s core.Sandbox) string {
	switch {
	case s.Providers == "":
		return "all (old sandbox)"
	case s.APIKeysInside:
		return s.Providers + "*"
	}
	return s.Providers
}

// APIKeys says in words where a sandbox's providers' API keys are.
func APIKeys(s core.Sandbox) string {
	switch {
	case !s.APIKeysInside && s.Gateway:
		return "on this machine, behind a gateway"
	case !s.APIKeysInside:
		return "none"
	case s.Gateway:
		return "inside the sandbox for a provider the gateway can't keep them for (see opencode-sandbox health); the rest on this machine"
	}
	return "inside the sandbox (it has no gateway)"
}

// Source is the folder a sandbox was copied from, as shown: with ~ for your
// home, or "nothing" if it started empty.
func Source(s core.Sandbox) string {
	if s.Source == "" {
		return "nothing"
	}
	return Tilde(s.Source)
}

// Network is a sandbox's network as listed: open or none.
func Network(s core.Sandbox) string {
	if s.NoNetwork {
		return "none"
	}
	return "open"
}

// NetworkWords says in words what a sandbox can reach.
func NetworkWords(s core.Sandbox) string {
	if s.NoNetwork {
		return "none: it reaches only its providers, through the gateway"
	}
	return "open: the internet, your network and services on this machine"
}

// RunsIn says in words what a sandbox runs in.
func RunsIn(s core.Sandbox) string {
	switch s.RunsIn {
	case "apple":
		return "a VM of its own (Apple container)"
	case "gvisor":
		return "Docker, on gVisor"
	case "kata":
		return "Docker, in a Kata VM"
	}
	return "Docker"
}

// Tilde shortens a path in your home folder to ~/….
func Tilde(p string) string {
	h := state.Home()
	if h != "" && h != "/" && (p == h || strings.HasPrefix(p, h+"/")) {
		return "~" + p[len(h):]
	}
	return p
}

// Table lists sandboxes, as `ls` prints them.
func (t *Theme) Table(ss []core.Sandbox) string {
	tb := table.New().
		Border(lipgloss.HiddenBorder()).BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderHeader(false).BorderColumn(false).
		Headers("", "NAME", "STATE", "RUNS IN", "NETWORK", "SSH KEY", "PROVIDER", "CREATED", "COPIED FROM").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingRight(2)
			if col == 0 {
				s = s.PaddingRight(1)
			}
			if row == table.HeaderRow {
				return s.Inherit(t.Dim).Bold(true)
			}
			return s
		})
	for _, s := range ss {
		tb.Row(t.Badge(s), t.Bold.Render(s.Name), t.StatusStyled(s), s.RunsIn, Network(s), KeyName(s), Providers(s),
			t.Dim.Render(s.Created), t.Dim.Render(Source(s)))
	}
	return strings.TrimRight(tb.Render(), "\n ")
}

// Health renders the health report, wrapped to width.
func (t *Theme) Health(d health.Doc, width int) string {
	if width <= 0 || width > 100 {
		width = 100
	}
	var b strings.Builder
	b.WriteString(t.Title.Render(d.Title) + "\n")
	const tagWidth = 12
	textWidth := max(width-tagWidth-4, 30)
	for _, s := range d.Sections {
		b.WriteString("\n" + t.Bold.Render(s.Title) + "\n")
		for _, it := range s.Items {
			tag := t.healthTag(it.Tag)
			text := lipgloss.Wrap(strings.ReplaceAll(it.Text, health.NBSP, string(lipgloss.NBSP)), textWidth, " ")
			text = strings.ReplaceAll(text, string(lipgloss.NBSP), " ")
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				left := strings.Repeat(" ", tagWidth)
				if i == 0 {
					left = tag + strings.Repeat(" ", max(tagWidth-lipgloss.Width(tag), 1))
				}
				b.WriteString("  " + left + line + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *Theme) healthTag(tag string) string {
	switch tag {
	case "ok":
		return t.fg(t.OK).Render("✓")
	case "no":
		return t.fg(t.Err).Render("✗")
	case "--":
		return t.fg(t.Warn).Render("!")
	case "used":
		return t.fg(t.OK).Bold(true).Render("● used")
	case "available":
		return t.fg(t.Accent).Render("○ available")
	case "install", "start", "set up":
		return t.fg(t.Warn).Render("↓ " + tag)
	case "trade-off":
		return t.fg(t.Muted).Render("⇄ trade-off")
	}
	return t.Dim.Render(tag)
}

// Message lines, as the command line prints them.
func (t *Theme) Success(msg string) string { return t.fg(t.OK).Render("✓") + " " + msg }
func (t *Theme) Warning(msg string) string { return t.fg(t.Warn).Render("!") + " " + msg }
func (t *Theme) Failure(msg string) string {
	return t.fg(t.Err).Render("✗") + " " + t.fg(t.Err).Render(msg)
}
func (t *Theme) Info(msg string) string { return t.Dim.Render("› " + msg) }

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return tty.IsTerminal(f.Fd()) }
