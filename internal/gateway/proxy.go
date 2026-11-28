package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Port is where the gateway listens inside a sandbox, on 127.0.0.1.
const Port = 17872

// Route is one provider or MCP server as the gateway serves it: where it
// is, and the secrets your opencode config has for it. A sandbox's config
// has Token in their place.
type Route struct {
	Base *url.URL
	// Key is a provider's API key; "" if your config has none.
	Key string
	// Headers are the headers your config sets for it, by their canonical
	// names.
	Headers map[string]string
}

// Table is what a gateway serves: a sandbox's providers, by id, and the MCP
// servers of your opencode config, by name.
type Table struct {
	Providers, MCP map[string]Route
}

// keyHeaders are the headers an API key travels in: as it is, or after a
// scheme ("Bearer KEY").
var keyHeaders = map[string]bool{"Authorization": true, "X-Api-Key": true, "Api-Key": true, "X-Goog-Api-Key": true}

// The kinds of things the gateway serves, as the first part of a path.
const (
	kindProvider = "provider"
	kindMCP      = "mcp"
)

// methods are the ones passed on, for each kind: what talking to a model
// takes, and for an MCP server also DELETE, which ends a session. The
// others can do harm with your key, or make a server send the request back
// with the key in it (TRACE).
var methods = map[string][]string{
	kindProvider: {http.MethodGet, http.MethodHead, http.MethodPost},
	kindMCP:      {http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete},
}

func passedOn(kind, method string) bool {
	for _, m := range methods[kind] {
		if m == method {
			return true
		}
	}
	return false
}

// Proxy passes a sandbox's requests on to its providers and MCP servers. A
// request for /provider/ID/chat/completions goes to provider ID's API, one
// for /mcp/NAME to MCP server NAME, with the real secrets put where the
// sandbox's config had Token; only there, so that nothing the sandbox sends
// can make a server echo a secret back.
type Proxy struct {
	// Token stands for a secret in the sandbox's config.
	Token func() string
	// Routes are what the sandbox reaches through the gateway.
	Routes func() Table
	// Transport reaches the servers; nil for DefaultTransport.
	Transport http.RoundTripper
	// Log is told about each request, without its secrets; may be nil.
	Log func(format string, args ...any)
}

// DefaultTransport reaches servers as any program on this machine would
// (it honours HTTPS_PROXY), and leaves what they send as it is.
func DefaultTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableCompression = true
	return t
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func refuse(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "opencode-sandbox: " + msg}})
}

// split takes a request path apart: the kind of thing it's for, which one,
// and the path within it, still escaped. A path that names no kind is a
// provider's, as sandboxes' configs had them before 0.13.0 (/ID/…). Paths
// that could climb out ("..", an escaped slash) are refused.
func split(escaped string) (kind, id, rest string, ok bool) {
	segs := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	plain := make([]string, len(segs))
	for i, seg := range segs {
		p, err := url.PathUnescape(seg)
		if err != nil || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return "", "", "", false
		}
		plain[i] = p
	}
	kind = kindProvider
	if _, known := methods[plain[0]]; known && len(segs) > 1 {
		kind, segs, plain = plain[0], segs[1:], plain[1:]
	}
	return kind, plain[0], strings.Join(segs[1:], "/"), plain[0] != ""
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	kind, id, rest, ok := split(r.URL.EscapedPath())
	if !ok {
		refuse(w, http.StatusBadRequest, "not a path the gateway passes on")
		return
	}
	if !passedOn(kind, r.Method) {
		ms := methods[kind]
		w.Header().Set("Allow", strings.Join(ms, ", "))
		refuse(w, http.StatusMethodNotAllowed, "the gateway passes on "+strings.Join(ms[:len(ms)-1], ", ")+" and "+ms[len(ms)-1]+" requests only, not "+r.Method)
		return
	}
	table := p.Routes()
	routes, what := table.Providers, "provider"
	if kind == kindMCP {
		routes, what = table.MCP, "MCP server"
	}
	route, ok := routes[id]
	if !ok {
		refuse(w, http.StatusNotFound, "this sandbox has no "+what+" '"+id+"' behind its gateway")
		return
	}
	token, start := p.Token(), time.Now()
	status := 0
	rp := &httputil.ReverseProxy{
		Transport:     p.Transport,
		FlushInterval: -1, // answers are streamed
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := *route.Base
			// An MCP server is at its address, to the letter; an API is
			// under its address.
			if kind != kindMCP || rest != "" {
				u.Path = strings.TrimRight(u.Path, "/")
				u.RawPath = strings.TrimRight(u.EscapedPath(), "/")
			}
			if rest != "" {
				plain, _ := url.PathUnescape(rest)
				u.Path += "/" + plain
				u.RawPath += "/" + rest
			}
			q := u.RawQuery
			u.RawQuery = r.URL.RawQuery
			if q != "" {
				u.RawQuery = strings.TrimPrefix(q+"&"+r.URL.RawQuery, "&")
				u.RawQuery = strings.TrimSuffix(u.RawQuery, "&")
			}
			pr.Out.URL = &u
			pr.Out.Host = u.Host
			restore(pr.Out.Header, route, token)
			if user := u.User; user != nil && pr.Out.Header.Get("Authorization") == "" {
				// A name and password in the address, sent as a browser would.
				pw, _ := user.Password()
				pr.Out.SetBasicAuth(user.Username(), pw)
			}
			u.User = nil
		},
		ModifyResponse: func(resp *http.Response) error {
			status = resp.StatusCode
			if route.Base.RawQuery != "" || route.Base.User != nil {
				// The address has a secret in it, and a server that sends
				// its client elsewhere may say where with the address it
				// was asked at.
				resp.Header.Del("Location")
				resp.Header.Del("Content-Location")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status = http.StatusBadGateway
			p.logf("%s %s: %v", what, id, err)
			refuse(w, http.StatusBadGateway, "can't reach "+what+" '"+id+"' from your machine: "+err.Error())
		},
	}
	if rp.Transport == nil {
		rp.Transport = DefaultTransport()
	}
	rp.ServeHTTP(w, r)
	p.logf("%s %s %s /%s -> %d in %s", what, id, r.Method, rest, status, time.Since(start).Round(time.Millisecond))
}

// restore puts the route's secrets where the token stands for them: a
// header your config sets, when it's the token; and the API key in a header
// keys travel in, when it's the token, alone or after a scheme. A token
// anywhere else stays a token.
func restore(h http.Header, route Route, token string) {
	if token == "" {
		return
	}
	for name, vals := range h {
		for i, v := range vals {
			if real, ok := route.Headers[name]; ok && v == token {
				vals[i] = real
				continue
			}
			if route.Key == "" || !keyHeaders[name] {
				continue
			}
			if v == token {
				vals[i] = route.Key
			} else if scheme, cred, ok := strings.Cut(v, " "); ok && cred == token && scheme != "" && !strings.ContainsAny(scheme, " \t,=") {
				vals[i] = scheme + " " + route.Key
			}
		}
	}
}
