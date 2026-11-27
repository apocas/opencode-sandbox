package tui

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// newForm holds what the new-sandbox form asks.
type newForm struct {
	folder, name, key string
	providers         []string
	defaults          map[string]string // a default name for each folder
}

// startForm opens the new-sandbox form.
func (m *model) startForm() tea.Cmd {
	if m.busy != "" {
		m.say(warn, "wait until %s finishes", strings.ToLower(m.busy))
		return nil
	}
	if err := m.c.CanCreate(); err != nil {
		m.say(bad, "%v", err)
		return nil
	}
	ps, err := m.c.ProviderChoices()
	if err != nil {
		m.say(bad, "%v", err)
		return nil
	}
	wd, _ := os.Getwd()
	nf := &newForm{folder: ui.Tilde(wd), defaults: map[string]string{}}
	c := m.c
	defaultName := func() string {
		ws, err := c.ResolveWorkspace(expand(nf.folder))
		if err != nil {
			return "the folder's name"
		}
		if n, ok := nf.defaults[ws]; ok {
			return n
		}
		n := c.DefaultName(ws)
		nf.defaults[ws] = n
		return n
	}
	choices, why := c.KeyChoices()
	keyOpts := make([]huh.Option[string], len(choices))
	for i, k := range choices {
		keyOpts[i] = huh.NewOption(k.Label+"  "+m.t.Dim.Render(k.Detail), k.Value)
	}
	nf.key = "new"
	fields := []huh.Field{
		huh.NewInput().Title("Folder to copy").
			Description("copied into /workspace, never mounted").
			Value(&nf.folder).
			SuggestionsFunc(func() []string { return dirSuggestions(nf.folder) }, &nf.folder).
			Validate(func(v string) error { _, err := c.ResolveWorkspace(expand(v)); return err }),
		huh.NewInput().Title("Name").
			PlaceholderFunc(defaultName, &nf.folder).
			Value(&nf.name).
			Validate(func(v string) error {
				if v == "" {
					return nil
				}
				return c.CheckName(v)
			}),
		huh.NewSelect[string]().Title("SSH key").Description(why).Options(keyOpts...).Value(&nf.key),
	}
	if len(ps) > 1 {
		opts := make([]huh.Option[string], len(ps))
		for i, p := range ps {
			opts[i] = huh.NewOption(p.ID+"  "+m.t.Dim.Render(p.Description), p.ID)
		}
		fields = append(fields, huh.NewMultiSelect[string]().Title("Providers").
			Description("space selects; the sandbox gets only these and their API keys").
			Options(opts...).Value(&nf.providers).
			Validate(func(v []string) error {
				if len(v) == 0 {
					return errors.New("pick at least one")
				}
				return nil
			}))
	}
	m.nf = nf
	m.form = huh.NewForm(huh.NewGroup(fields...).Title("New sandbox")).
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithShowHelp(true).WithWidth(min(max(m.w-8, 40), 90))
	m.mode = formMode
	return m.form.Init()
}

func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f, cmd := m.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.form = ff
	}
	switch m.form.State {
	case huh.StateAborted:
		m.mode, m.form = listMode, nil
		m.say(info, "no sandbox created")
		return m, nil
	case huh.StateCompleted:
		nf, c := m.nf, m.c
		m.mode, m.form = progressMode, nil
		ws, _ := c.ResolveWorkspace(expand(nf.folder))
		name := nf.name
		if name == "" {
			name = nf.defaults[ws]
		}
		o := core.CreateOptions{Workspace: expand(nf.folder), Name: name, Key: nf.key, Providers: nf.providers}
		m.prog = progress{title: "Creating sandbox '" + name + "' from " + ui.Tilde(ws)}
		return m, m.background("Creating '"+name+"'", func() tea.Msg {
			created, err := c.Create(o)
			return createdMsg{created, err}
		})
	}
	return m, cmd
}

// dirSuggestions completes the folder being typed.
func dirSuggestions(typed string) []string {
	full := expand(typed)
	dir, base := filepath.Split(full)
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), base) || strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		out = append(out, typed[:len(typed)-len(base)]+e.Name()+"/")
	}
	sort.Strings(out)
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
