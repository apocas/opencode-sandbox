package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/occonfig"
)

// apis is where the APIs of the AI SDK's packages are, for a provider whose
// options name no baseURL: the packages' own defaults.
var apis = map[string]string{
	"@ai-sdk/anthropic":           "https://api.anthropic.com/v1",
	"@ai-sdk/openai":              "https://api.openai.com/v1",
	"@ai-sdk/google":              "https://generativelanguage.googleapis.com/v1beta",
	"@ai-sdk/groq":                "https://api.groq.com/openai/v1",
	"@ai-sdk/mistral":             "https://api.mistral.ai/v1",
	"@ai-sdk/xai":                 "https://api.x.ai/v1",
	"@openrouter/ai-sdk-provider": "https://openrouter.ai/api/v1",
}

// packages are the packages opencode uses for the providers it knows by
// name, when your config names none.
var packages = map[string]string{
	"anthropic": "@ai-sdk/anthropic", "openai": "@ai-sdk/openai", "google": "@ai-sdk/google",
	"groq": "@ai-sdk/groq", "mistral": "@ai-sdk/mistral", "xai": "@ai-sdk/xai",
	"openrouter": "@openrouter/ai-sdk-provider",
}

// Switch is the environment variable that, set to "off", gives new sandboxes
// their providers' API keys instead of a gateway, as before 0.9.0.
const Switch = "OPENCODE_SANDBOX_GATEWAY"

// Wanted says whether new sandboxes get a gateway, given Switch's value.
func Wanted(v string) bool {
	switch strings.ToLower(v) {
	case "off", "0", "no", "false":
		return false
	}
	return true
}

// Env is what your opencode config may refer to: the environment, for
// {env:NAME}, and the folder the config is in, for {file:PATH}.
type Env struct {
	Getenv func(string) string
	Dir    string
	Home   string
}

var (
	envVar  = regexp.MustCompile(`\{env:([^}]+)\}`)
	fileVar = regexp.MustCompile(`\{file:([^}]+)\}`)
)

// resolve fills in {env:NAME} and {file:PATH} as opencode does: a variable
// that isn't set is empty, a file is read without the space around it
// (relative to the config's folder, ~ being your home), and one that can't
// be read is empty.
func (e Env) resolve(s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	s = envVar.ReplaceAllStringFunc(s, func(m string) string {
		if e.Getenv == nil {
			return ""
		}
		return e.Getenv(envVar.FindStringSubmatch(m)[1])
	})
	return fileVar.ReplaceAllStringFunc(s, func(m string) string {
		p := fileVar.FindStringSubmatch(m)[1]
		switch {
		case p == "~" || strings.HasPrefix(p, "~/"):
			p = filepath.Join(e.Home, p[1:])
		case !filepath.IsAbs(p):
			p = filepath.Join(e.Dir, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	})
}

// What makes an option's name that of a secret, and what makes it only point
// at one (a file, an id, the name of a variable).
var (
	secretName = regexp.MustCompile(`(?i)key|secret|token|password|passwd|credential|authorization`)
	notSecret  = regexp.MustCompile(`(?i)^public|(file|filename|path|env|name|url|uri|id)$`)
)

// secret says whether an option holds a secret that would go into a sandbox:
// one named like a secret, with a value that isn't empty and isn't left to
// {env:NAME} or {file:PATH}, which aren't there inside a sandbox.
func secret(name, value string) bool {
	return value != "" && !strings.Contains(value, "{env:") && !strings.Contains(value, "{file:") &&
		secretName.MatchString(name) && !notSecret.MatchString(name)
}

// others lists what in a provider's config looks like a secret and isn't one
// the gateway keeps: an option other than apiKey and headers (a cloud's
// secretAccessKey, say), and an option or a header of one of its models.
func others(p *occonfig.Object) []string {
	var found []string
	var walk func(v any, at string, top bool)
	walk = func(v any, at string, top bool) {
		switch v := v.(type) {
		case *occonfig.Object:
			for _, k := range v.Keys() {
				if top && (k == "apiKey" || k == "headers") {
					continue // the gateway keeps these
				}
				c, _ := v.Get(k)
				if s, ok := c.(string); ok {
					if secret(k, s) {
						found = append(found, at+"."+k)
					}
					continue
				}
				walk(c, at+"."+k, false)
			}
		case []any:
			for _, c := range v {
				walk(c, at, false)
			}
		}
	}
	o, _ := p.Get("options")
	walk(o, "options", true)
	ms, _ := p.Get("models")
	models, _ := ms.(*occonfig.Object)
	if models == nil {
		return found
	}
	for _, name := range models.Keys() {
		v, _ := models.Get(name)
		m, _ := v.(*occonfig.Object)
		if m == nil {
			continue
		}
		at := "models." + name
		o, _ := m.Get("options")
		walk(o, at+".options", false)
		h, _ := m.Get("headers")
		if hs, ok := h.(*occonfig.Object); ok {
			for _, k := range hs.Keys() {
				c, _ := hs.Get(k)
				if s, _ := c.(string); secret(k, s) {
					found = append(found, at+".headers."+k)
				}
			}
		}
	}
	return found
}

// NoAddress is why the gateway can't serve a provider it doesn't know the
// API of.
const NoAddress = "your opencode config doesn't say where its API is (options.baseURL)"

// notKept says which secrets of a provider the gateway doesn't keep.
func notKept(found []string) string {
	list := strings.Join(found[:min(len(found), 3)], ", ")
	if n := len(found) - 3; n > 0 {
		list += fmt.Sprintf(" and %d more", n)
	}
	return "the gateway keeps a provider's options.apiKey and options.headers, not its " + list
}

// Routes reads your opencode config: for each provider in ids, where its API
// is and the secrets your config has for it. inside says, for a provider
// with a secret that goes into its sandbox's config all the same, why: the
// gateway can't serve it, or the secret isn't one it keeps. A provider can
// be in both. One without secrets needs no gateway and is in neither, unless
// all is set: a sandbox without a network reaches every provider through
// the gateway, so each one whose API's address is known is served.
func Routes(root any, ids []string, env Env, all bool) (routes map[string]Route, inside map[string]string) {
	routes, inside = map[string]Route{}, map[string]string{}
	top, _ := root.(*occonfig.Object)
	if top == nil {
		return
	}
	ps, _ := top.Get("provider")
	provs, _ := ps.(*occonfig.Object)
	if provs == nil {
		return
	}
	for _, id := range ids {
		v, _ := provs.Get(id)
		p, _ := v.(*occonfig.Object)
		if p == nil {
			continue
		}
		str := func(o *occonfig.Object, k string) string {
			if o == nil {
				return ""
			}
			v, _ := o.Get(k)
			s, _ := v.(string)
			return s
		}
		o, _ := p.Get("options")
		opts, _ := o.(*occonfig.Object)
		r := Route{Key: env.resolve(str(opts, "apiKey")), Headers: map[string]string{}}
		if opts != nil {
			h, _ := opts.Get("headers")
			if hs, ok := h.(*occonfig.Object); ok {
				for _, k := range hs.Keys() {
					if s := str(hs, k); s != "" {
						r.Headers[http.CanonicalHeaderKey(k)] = env.resolve(s)
					}
				}
			}
		}
		extra := others(p)
		keeps := r.Key != "" || len(r.Headers) > 0
		if !keeps && !all {
			if len(extra) > 0 {
				inside[id] = notKept(extra)
			}
			continue // nothing for the gateway to keep
		}
		base := env.resolve(str(opts, "baseURL"))
		if base == "" {
			base = env.resolve(str(p, "api"))
		}
		if base == "" {
			pkg := str(p, "npm")
			if pkg == "" {
				pkg = packages[id]
			}
			base = apis[pkg]
		}
		why := ""
		u, err := url.Parse(base)
		switch {
		case base == "":
			why = NoAddress
		case err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http":
			why = fmt.Sprintf("its API's address (%s) isn't an http or https URL", base)
		}
		if why != "" {
			switch {
			case keeps:
				inside[id] = why
			case len(extra) > 0:
				inside[id] = notKept(extra)
			}
			continue
		}
		r.Base = u
		routes[id] = r
		if len(extra) > 0 {
			inside[id] = notKept(extra)
		}
	}
	return
}

// Rewrite changes a sandbox's config (see occonfig.Filter) so that the
// providers in routes go through the gateway: each one's API is the gateway
// inside the sandbox, and token stands where its secrets were.
func Rewrite(cfg *occonfig.Object, routes map[string]Route, token string) {
	v, _ := cfg.Get("provider")
	provs, _ := v.(*occonfig.Object)
	if provs == nil {
		return
	}
	for _, id := range provs.Keys() {
		route, ok := routes[id]
		if !ok {
			continue
		}
		v, _ := provs.Get(id)
		p, ok := v.(*occonfig.Object)
		if !ok {
			continue
		}
		p = p.Clone()
		opts := occonfig.NewObject()
		if o, _ := p.Get("options"); o != nil {
			if oo, ok := o.(*occonfig.Object); ok {
				opts = oo.Clone()
			}
		}
		opts.Set("baseURL", fmt.Sprintf("http://127.0.0.1:%d/%s/%s", Port, kindProvider, url.PathEscape(id)))
		if route.Key != "" {
			opts.Set("apiKey", token)
		}
		if h, _ := opts.Get("headers"); h != nil {
			hs, ok := h.(*occonfig.Object)
			if !ok {
				hs = occonfig.NewObject()
			}
			hs = hs.Clone()
			for _, k := range hs.Keys() {
				if _, secret := route.Headers[http.CanonicalHeaderKey(k)]; secret {
					hs.Set(k, token)
				}
			}
			opts.Set("headers", hs)
		}
		p.Set("options", opts)
		p.Delete("api") // the gateway is its API now
		provs.Set(id, p)
	}
}
