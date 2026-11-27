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

// Routes reads your opencode config: for each provider in ids, where its API
// is and the secrets your config has for it. skipped says why a provider
// that has secrets can't be served, and so keeps them in its sandbox's
// config; one without secrets needs no gateway and is in neither.
func Routes(root any, ids []string, env Env) (routes map[string]Route, skipped map[string]string) {
	routes, skipped = map[string]Route{}, map[string]string{}
	top, _ := root.(*occonfig.Object)
	if top == nil {
		return
	}
	all, _ := top.Get("provider")
	provs, _ := all.(*occonfig.Object)
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
		if r.Key == "" && len(r.Headers) == 0 {
			continue // nothing to keep out of the sandbox
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
		if base == "" {
			skipped[id] = "your opencode config doesn't say where its API is (options.baseURL)"
			continue
		}
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
			skipped[id] = fmt.Sprintf("its API's address (%s) isn't an http or https URL", base)
			continue
		}
		r.Base = u
		routes[id] = r
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
		opts.Set("baseURL", fmt.Sprintf("http://127.0.0.1:%d/%s", Port, url.PathEscape(id)))
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
