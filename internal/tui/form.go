package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/gateway"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

// newForm holds what the new-sandbox form asks.
type newForm struct {
	folder, name, key string
	providers         []string
	defaults          map[string]string // a default name for each folder

	// more: the options that have defaults are asked too (ctrl+o).
	more bool
	// network is "open" or "none", place where it runs (see placeOf), and
	// gateway "on" or "off": what CreateOptions takes.
	network, place, gateway string

	mu   sync.Mutex
	dots map[string]core.DotFiles // the dot files of each folder looked at
}

// dotFiles is what DotFiles finds in a folder, looked for once.
func (nf *newForm) dotFiles(c *core.Core, ws string) core.DotFiles {
	nf.mu.Lock()
	defer nf.mu.Unlock()
	d, ok := nf.dots[ws]
	if !ok {
		d = c.DotFiles(ws)
		nf.dots[ws] = d
	}
	return d
}

// empty says whether the folder field is left empty: nothing is copied.
func (nf *newForm) empty() bool { return strings.TrimSpace(nf.folder) == "" }

// workspace is the folder typed so far, checked; "" where nothing is copied.
func (nf *newForm) workspace(c *core.Core) (string, error) {
	if nf.empty() {
		return "", nil
	}
	return c.ResolveWorkspace(expand(nf.folder))
}

// about describes the folder typed so far: what happens to it, and the dot
// files it has, which are copied too.
func (nf *newForm) about(c *core.Core) string {
	const copied = "copied into /workspace, never mounted; leave empty to copy nothing"
	if nf.empty() {
		return "nothing is copied: /workspace starts empty"
	}
	ws, err := nf.workspace(c)
	if err != nil {
		return copied
	}
	if d := nf.dotFiles(c, ws); len(d.Found) > 0 {
		// On a line of its own, short enough not to be wrapped: the form
		// would break it in the middle of a name, at a dot.
		line := []rune("its dot files too, which often hold secrets: " + strings.ReplaceAll(d.List(3), ", ", " · "))
		if len(line) > 76 {
			line = append(line[:75], '…')
		}
		return copied + "\n" + string(line)
	}
	return copied
}

// startForm opens the new-sandbox form.
func (m *model) startForm() tea.Cmd {
	if m.isBusy() {
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
	nf := &newForm{folder: ui.Tilde(wd), defaults: map[string]string{}, dots: map[string]core.DotFiles{}}
	c := m.c
	defaultName := func() string {
		ws, err := nf.workspace(c)
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
	// What new sandboxes get when nothing more is asked.
	nf.network, _ = core.NetworkFor("")
	nf.gateway = "on"
	if !gateway.Wanted(os.Getenv(gateway.Switch)) {
		nf.gateway = "off"
	}
	fields := []huh.Field{
		huh.NewInput().Title("Folder to copy").
			DescriptionFunc(func() string { return nf.about(c) }, &nf.folder).
			Value(&nf.folder).
			SuggestionsFunc(func() []string { return dirSuggestions(nf.folder) }, &nf.folder).
			Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return nil // nothing is copied
				}
				_, err := c.ResolveWorkspace(expand(v))
				return err
			}),
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
		// Height: the list's own, plus title and description. Left unset,
		// huh takes those two lines off the list and hides its options.
		fields = append(fields, huh.NewMultiSelect[string]().Title("Providers the sandbox may use").
			Description("space selects one or more").
			Options(opts...).Height(min(len(opts), 8)+2).Value(&nf.providers).
			Validate(func(v []string) error {
				if len(v) == 0 {
					return errors.New("pick at least one")
				}
				return nil
			}))
	}
	m.nf = nf
	// In a text field, tab completes the folder being typed, as in the copy
	// prompt and a shell (huh's own key for that is ctrl+e); enter moves on.
	// Where there's nothing to complete, tab moves on too (see updateForm).
	km := huh.NewDefaultKeyMap()
	km.Input.AcceptSuggestion = key.NewBinding(key.WithKeys("tab", "ctrl+e"), key.WithHelp("tab", "complete"))
	km.Input.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next"))
	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(km.MultiSelect.Toggle.Keys()...), key.WithHelp("space", "select"))
	// The footer shows the keys (see formHints), not the form itself.
	m.form = huh.NewForm(
		huh.NewGroup(fields...).Title("New sandbox"),
		huh.NewGroup(moreFields(m, nf)...).Title("New sandbox: more options").WithHideFunc(func() bool { return !nf.more }),
	).WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithKeyMap(km).WithShowHelp(false).WithWidth(min(max(m.w-8, 40), 90))
	m.mode = formMode
	if nf.network == "none" {
		m.say(info, "new sandboxes get no network (%s), so no SSH key whichever is picked; ctrl+o changes that", core.NetworkEnv)
	}
	return tea.Batch(m.form.Init(), m.sizeForm())
}

// options are what the form asks Create for, for a sandbox by that name.
func (nf *newForm) options(name string) core.CreateOptions {
	o := core.CreateOptions{Workspace: expand(nf.folder), Name: name, Key: nf.key, Providers: nf.providers,
		Network: nf.network, Gateway: nf.gateway}
	if nf.empty() {
		o.Workspace, o.Empty = "", true
	}
	o.Engine, o.Runtime = placeOf(nf.place)
	if o.Network == "none" {
		o.Key = "none" // nowhere to log in to
	}
	return o
}

// placeOf takes a place's value in the form apart: the engine and runtime
// CreateOptions takes.
func placeOf(v string) (engine, rt string) {
	engine, rt, _ = strings.Cut(v, "|")
	return
}

// moreFields are the options that have defaults: what the sandbox can
// reach, where it runs and where its providers' API keys are.
func moreFields(m *model, nf *newForm) []huh.Field {
	c, dim := m.c, m.t.Dim.Render
	var places []huh.Option[string]
	for _, p := range c.Places() {
		places = append(places, huh.NewOption(p.Label+"  "+dim(p.Detail), p.Engine+"|"+p.Runtime))
	}
	if len(places) == 0 { // (a form opened where nothing runs sandboxes)
		places = append(places, huh.NewOption("automatic", "|"))
	}
	nf.place = "|"
	return []huh.Field{
		huh.NewSelect[string]().Title("Network").
			Description("what the sandbox can reach").
			Options(
				huh.NewOption("open  "+dim("the internet, your network and services on this machine"), "open"),
				huh.NewOption("none  "+dim("only its providers, through the gateway: no SSH key, no downloads"), "none"),
			).Value(&nf.network),
		huh.NewSelect[string]().Title("Runs in").
			Description("what isolates it").
			Options(places...).Value(&nf.place).
			Validate(func(v string) error {
				engine, rt := placeOf(v)
				if err := c.CheckPlace(engine, rt); err != nil {
					return err
				}
				if nf.network == "none" || nf.key == "new" || nf.key == "none" {
					return nil
				}
				choices, _ := c.KeyChoicesFor(engine, rt)
				for _, k := range choices {
					if k.Value == nf.key {
						return nil
					}
				}
				return fmt.Errorf("the SSH key picked before (%s) would have to be copied into a sandbox there; go back and pick a disposable key or none", nf.key)
			}),
		huh.NewSelect[string]().Title("API keys").
			Description("where its providers' API keys are").
			Options(
				huh.NewOption("on this machine  "+dim("a gateway here puts them into its requests"), "on"),
				huh.NewOption("inside the sandbox  "+dim("in its config, where the agent can read them"), "off"),
			).Value(&nf.gateway).
			Validate(func(v string) error {
				if v == "off" && nf.network == "none" {
					return errors.New("without a network, a sandbox reaches its providers through the gateway only")
				}
				return nil
			}),
	}
}

// sizeForm tells the form how much room it has, so it shows every field
// that fits and scrolls to the one in use otherwise.
func (m *model) sizeForm() tea.Cmd {
	if m.form == nil || m.w == 0 {
		return nil
	}
	// header, status and footer (up to two lines), and the form's padding
	f, cmd := m.form.Update(tea.WindowSizeMsg{Width: m.w, Height: max(m.h-7, 6)})
	if ff, ok := f.(*huh.Form); ok {
		m.form = ff
	}
	return cmd
}

// formUses reports whether the field in use has a job for key right now,
// e.g. esc while filtering a list.
func formUses(f *huh.Form, k string) bool {
	if f == nil {
		return false
	}
	for _, b := range f.KeyBinds() {
		if !b.Enabled() {
			continue
		}
		for _, bk := range b.Keys() {
			if bk == k {
				return true
			}
		}
	}
	return false
}

// formEnter says what enter does in the field in use: "next", "select",
// "submit"…
func formEnter(f *huh.Form) string {
	for _, b := range f.KeyBinds() {
		if !b.Enabled() {
			continue
		}
		for _, k := range b.Keys() {
			if k == "enter" {
				return b.Help().Desc
			}
		}
	}
	return ""
}

// formHints lists the keys of the field in use, then how to leave.
func formHints(f *huh.Form) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	for _, b := range f.KeyBinds() {
		h := b.Help()
		if !b.Enabled() || h.Key == "" || seen[h.Key+h.Desc] {
			continue
		}
		seen[h.Key+h.Desc] = true
		desc := h.Desc
		switch desc {
		case "back":
			desc = "previous field"
		case "submit":
			desc = "create"
		}
		out = append(out, [2]string{h.Key, desc})
	}
	if !formUses(f, "esc") {
		out = append(out, [2]string{"esc", "cancel"})
	}
	return out
}

func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ctrl+o asks the options that have defaults too, after the last field.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+o" && !m.nf.more {
		m.nf.more = true
		m.form.UpdateFieldPositions()
		m.say(info, "more options follow the last field: network, where it runs, API keys")
		return m, nil
	}
	// tab moves on from a text field with nothing to complete. Never from
	// the last field: there, moving on would create the sandbox.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "tab" && !formUses(m.form, "tab") {
		if _, text := m.form.GetFocusedField().(*huh.Input); text && formEnter(m.form) == "next" {
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		}
	}
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
		ws, _ := nf.workspace(c)
		name := nf.name
		if name == "" {
			name = nf.defaults[ws]
		}
		o := nf.options(name)
		m.prog = progress{title: "Creating sandbox '" + name + "' from " + ui.Tilde(ws)}
		if ws == "" {
			m.prog.title = "Creating sandbox '" + name + "', with nothing copied into it"
		}
		if d := nf.dotFiles(c, ws); len(d.Found) > 0 {
			m.prog.notes = append(m.prog.notes, prefix(true)+"copying its dot files too: "+d.List(4))
		}
		return m, m.background("Creating '"+name+"'", func() tea.Msg {
			created, err := c.Create(o)
			return createdMsg{created, err}
		})
	}
	return m, cmd
}

// dirSuggestions completes the folder being typed.
func dirSuggestions(typed string) []string {
	if typed == "~" {
		return []string{"~/"}
	}
	head, base := filepath.Split(typed)
	if head == "" {
		return nil
	}
	ents, err := os.ReadDir(expand(head))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), base) || strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		out = append(out, head+e.Name()+"/")
	}
	sort.Strings(out)
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
