// Package occonfig reads your opencode config and writes the filtered copy a
// sandbox gets.
package occonfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Load reads and parses an opencode config (plain JSON).
func Load(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// ProviderIDs lists the providers in the config, in order.
func ProviderIDs(root any) []string {
	if p := providers(root); p != nil {
		return p.Keys()
	}
	return nil
}

// Provider is one entry of the provider menu.
type Provider struct {
	ID          string
	Description string // e.g. "My provider (2 models)"
}

// Menu describes each provider for the interactive choice.
func Menu(root any) []Provider {
	p := providers(root)
	if p == nil {
		return nil
	}
	var out []Provider
	for _, id := range p.Keys() {
		v, _ := p.Get(id)
		name, models := "", 0
		if o, ok := v.(*Object); ok {
			n, _ := o.Get("name")
			name = text(n)
			switch m, _ := o.Get("models"); m := m.(type) {
			case *Object:
				models = m.Len()
			case []any:
				models = len(m)
			}
		}
		count := "1 model"
		if models != 1 {
			count = fmt.Sprintf("%d models", models)
		}
		out = append(out, Provider{ID: id, Description: fmt.Sprintf("%s (%s)", name, count)})
	}
	return out
}

func providers(root any) *Object {
	o, ok := root.(*Object)
	if !ok {
		return nil
	}
	p, _ := o.Get("provider")
	po, _ := p.(*Object)
	return po
}

// text renders a value as jq's string interpolation does, with null and
// false as "" (the `// ""` default).
func text(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case bool:
		if v {
			return "true"
		}
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return strings.TrimRight(string(Encode(v)), "\n")
	}
}

// Filter returns the config a sandbox gets: only the providers in keep;
// opencode's built-in providers switched off (its free "opencode" provider
// would otherwise be offered too); model settings that point at a provider
// the sandbox doesn't get dropped; and, when no model is left, the first
// model of the first provider. It is the bash launcher's jq program,
// unchanged in behaviour.
func Filter(root any, keep []string) (*Object, error) {
	in, ok := root.(*Object)
	if !ok {
		return nil, fmt.Errorf("the config isn't a JSON object")
	}
	out := in.Clone()

	allowed := func(id string) bool {
		for _, k := range keep {
			if k == id {
				return true
			}
		}
		return false
	}
	// A model setting like "provider/model" is fine if its provider is kept.
	prune := func(o *Object, key string) *Object {
		if m, ok := o.vals[key].(string); ok && !allowed(strings.SplitN(m, "/", 2)[0]) {
			o = o.Clone()
			o.Delete(key)
		}
		return o
	}

	kept := NewObject()
	switch p, _ := in.Get("provider"); p := p.(type) {
	case *Object:
		for _, id := range p.keys {
			if allowed(id) {
				kept.Set(id, p.vals[id])
			}
		}
	case nil:
	case bool:
		if p {
			return nil, fmt.Errorf("provider must be an object")
		}
	default:
		return nil, fmt.Errorf("provider must be an object")
	}
	out.Set("provider", kept)

	ids := make([]any, len(keep))
	for i, k := range keep {
		ids[i] = k
	}
	out.Set("enabled_providers", ids)

	out = prune(out, "model")
	out = prune(out, "small_model")
	for _, section := range []string{"agent", "mode"} {
		if s, ok := out.vals[section].(*Object); ok {
			s = s.Clone()
			for _, k := range s.keys {
				if v, ok := s.vals[k].(*Object); ok {
					s.vals[k] = prune(v, "model")
				}
			}
			out.Set(section, s)
		}
	}

	if _, has := out.Get("model"); !has && len(keep) > 0 {
		if p, ok := kept.vals[keep[0]].(*Object); ok {
			if models, ok := p.vals["models"].(*Object); ok && models.Len() > 0 {
				out.Set("model", keep[0]+"/"+models.keys[0])
			}
		}
	}
	return out, nil
}
