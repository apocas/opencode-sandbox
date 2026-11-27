package ui

import (
	"errors"
	"os"

	"charm.land/huh/v2"
)

// ErrCancelled: the user backed out of a picker.
var ErrCancelled = errors.New("cancelled")

// Option is a choice in a picker.
type Option struct{ Value, Label, Detail string }

func (t *Theme) run(fields ...huh.Field) error {
	err := huh.NewForm(huh.NewGroup(fields...)).
		WithOutput(os.Stderr).WithShowHelp(true).
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCancelled
	}
	return err
}

func options(opts []Option) []huh.Option[string] {
	out := make([]huh.Option[string], len(opts))
	for i, o := range opts {
		label := o.Label
		if o.Detail != "" {
			label += "  " + o.Detail
		}
		out[i] = huh.NewOption(label, o.Value)
	}
	return out
}

// Pick asks for one of opts on the terminal.
func (t *Theme) Pick(title, desc string, opts []Option) (string, error) {
	var v string
	s := huh.NewSelect[string]().Title(title).Options(options(opts)...).Value(&v)
	if desc != "" {
		s.Description(desc)
	}
	return v, t.run(s)
}

// PickMany asks for one or more of opts on the terminal.
func (t *Theme) PickMany(title, desc string, opts []Option) ([]string, error) {
	var v []string
	m := huh.NewMultiSelect[string]().Title(title).Options(options(opts)...).Value(&v).
		Validate(func(v []string) error {
			if len(v) == 0 {
				return errors.New("pick at least one (space to select)")
			}
			return nil
		})
	if desc != "" {
		m.Description(desc)
	}
	return v, t.run(m)
}

// Confirm asks a yes/no question on the terminal; no is the default.
func (t *Theme) Confirm(title, desc string) (bool, error) {
	var v bool
	c := huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&v)
	if desc != "" {
		c.Description(desc)
	}
	return v, t.run(c)
}
