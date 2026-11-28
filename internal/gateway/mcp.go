package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/occonfig"
)

// mcpServer is one MCP server of an opencode config: its name, what it's
// set to, and the object that holds it.
type mcpServer struct {
	name string
	conf *occonfig.Object
	in   *occonfig.Object
}

// mcpServers finds the MCP servers of a config: under mcp.servers, and
// directly under mcp, where older configs have them (opencode reads both).
// A name is taken once, mcp.servers first.
func mcpServers(top *occonfig.Object) []mcpServer {
	if top == nil {
		return nil
	}
	v, _ := top.Get("mcp")
	m, _ := v.(*occonfig.Object)
	if m == nil {
		return nil
	}
	var out []mcpServer
	seen := map[string]bool{}
	add := func(in *occonfig.Object) {
		for _, name := range in.Keys() {
			v, _ := in.Get(name)
			conf, _ := v.(*occonfig.Object)
			if conf == nil || seen[name] {
				continue
			}
			if t, _ := conf.Get("type"); t != "remote" && t != "local" {
				continue
			}
			seen[name] = true
			out = append(out, mcpServer{name: name, conf: conf, in: in})
		}
	}
	if v, _ := m.Get("servers"); v != nil {
		if servers, ok := v.(*occonfig.Object); ok {
			if _, isServer := servers.Get("type"); !isServer {
				add(servers)
			}
		}
	}
	add(m)
	return out
}

func strOf(o *occonfig.Object, k string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(k)
	s, _ := v.(string)
	return s
}

// MCP reads the MCP servers of your opencode config. routes are the remote
// ones the gateway serves, by name: those with something to keep on this
// machine (the headers your config sets for them, or an address with a
// query or a password in it), and with all set, as for a sandbox without a
// network, every one with an address. inside says, for a server with a
// secret that goes into a sandbox's config all the same, why: it runs
// inside the sandbox, or the secret isn't one the gateway keeps.
func MCP(root any, env Env, all bool) (routes map[string]Route, inside map[string]string) {
	routes, inside = map[string]Route{}, map[string]string{}
	top, _ := root.(*occonfig.Object)
	for _, s := range mcpServers(top) {
		if t, _ := s.conf.Get("type"); t == "local" {
			// It runs inside the sandbox, with its environment.
			var found []string
			if e, _ := s.conf.Get("environment"); e != nil {
				if vars, ok := e.(*occonfig.Object); ok {
					for _, k := range vars.Keys() {
						if secret(k, strOf(vars, k)) {
							found = append(found, k)
						}
					}
				}
			}
			if len(found) > 0 {
				inside[s.name] = "it runs inside the sandbox, with " + some(found) + " in its environment"
			}
			continue
		}
		r := Route{Headers: map[string]string{}}
		if h, _ := s.conf.Get("headers"); h != nil {
			if hs, ok := h.(*occonfig.Object); ok {
				for _, k := range hs.Keys() {
					if v := strOf(hs, k); v != "" {
						r.Headers[http.CanonicalHeaderKey(k)] = env.resolve(v)
					}
				}
			}
		}
		// What signing in with OAuth takes isn't the gateway's to keep.
		oauth := ""
		if o, _ := s.conf.Get("oauth"); o != nil {
			if oo, ok := o.(*occonfig.Object); ok && secret("clientSecret", strOf(oo, "clientSecret")) {
				oauth = "the gateway keeps an MCP server's headers and address, not its oauth.clientSecret"
			}
		}
		raw := env.resolve(strOf(s.conf, "url"))
		u, err := url.Parse(raw)
		bad := raw == "" || err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http"
		keeps := len(r.Headers) > 0 || !bad && (u.User != nil || u.RawQuery != "")
		switch {
		case !keeps && !all:
			// Nothing to keep: the sandbox reaches it by itself.
		case bad && keeps:
			inside[s.name] = fmt.Sprintf("its address (%s) isn't an http or https URL", raw)
		case bad:
		default:
			r.Base = u
			routes[s.name] = r
		}
		if oauth != "" {
			inside[s.name] = oauth
		}
	}
	return
}

// some names up to three of what was found.
func some(found []string) string {
	sort.Strings(found)
	list := strings.Join(found[:min(len(found), 3)], ", ")
	if n := len(found) - 3; n > 0 {
		list += fmt.Sprintf(" and %d more", n)
	}
	return list
}

// RewriteMCP changes a sandbox's config so that the MCP servers in routes
// go through the gateway: each one's address is the gateway inside the
// sandbox, and token stands where its headers' values were.
func RewriteMCP(cfg *occonfig.Object, routes map[string]Route, token string) {
	if len(routes) == 0 {
		return
	}
	v, _ := cfg.Get("mcp")
	m, _ := v.(*occonfig.Object)
	if m == nil {
		return
	}
	// The config shares what it doesn't change with yours: copy what does.
	m = m.Clone()
	cfg.Set("mcp", m)
	if v, _ := m.Get("servers"); v != nil {
		if servers, ok := v.(*occonfig.Object); ok {
			if _, isServer := servers.Get("type"); !isServer {
				m.Set("servers", servers.Clone())
			}
		}
	}
	for _, s := range mcpServers(cfg) {
		route, ok := routes[s.name]
		if !ok {
			continue
		}
		conf := s.conf.Clone()
		conf.Set("url", fmt.Sprintf("http://127.0.0.1:%d/mcp/%s", Port, url.PathEscape(s.name)))
		if h, _ := conf.Get("headers"); h != nil {
			if hs, ok := h.(*occonfig.Object); ok {
				hs = hs.Clone()
				for _, k := range hs.Keys() {
					if _, kept := route.Headers[http.CanonicalHeaderKey(k)]; kept {
						hs.Set(k, token)
					}
				}
				conf.Set("headers", hs)
			}
		}
		s.in.Set(s.name, conf)
	}
}
