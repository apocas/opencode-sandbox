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

// Route is one provider as the gateway serves it: where its API is, and the
// secrets your opencode config has for it. A sandbox's config has Token in
// their place.
type Route struct {
	Base *url.URL
	// Key is the provider's API key; "" if your config has none.
	Key string
	// Headers are the headers your config sets for the provider, by their
	// canonical names.
	Headers map[string]string
}

// keyHeaders are the headers an API key travels in: as it is, or after a
// scheme ("Bearer KEY").
var keyHeaders = map[string]bool{"Authorization": true, "X-Api-Key": true, "Api-Key": true, "X-Goog-Api-Key": true}

// methods are the ones passed on: what talking to a model takes. The others
// can do harm with your key (DELETE), or make a server send the request back
// with the key in it (TRACE).
var methods = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodPost: true}

// Proxy passes a sandbox's requests on to its providers. A request for
// /ID/chat/completions goes to provider ID's API with the real secrets put
// where the sandbox's config had Token; only there, so that nothing the
// sandbox sends can make a provider echo a secret back.
type Proxy struct {
	// Token stands for a secret in the sandbox's config.
	Token func() string
	// Routes are the sandbox's providers, by id.
	Routes func() map[string]Route
	// Transport reaches the providers; nil for DefaultTransport.
	Transport http.RoundTripper
	// Log is told about each request, without its secrets; may be nil.
	Log func(format string, args ...any)
}

// DefaultTransport reaches providers as any program on this machine would
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

// split takes a request path apart: the provider it's for and the path
// within that provider's API, still escaped. Paths that could climb out of
// the API ("..", an escaped slash) are refused.
func split(escaped string) (id, rest string, ok bool) {
	segs := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	for i, seg := range segs {
		plain, err := url.PathUnescape(seg)
		if err != nil || plain == "." || plain == ".." || strings.ContainsAny(plain, `/\`) {
			return "", "", false
		}
		if i == 0 {
			id = plain
		}
	}
	return id, strings.Join(segs[1:], "/"), id != ""
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !methods[r.Method] {
		w.Header().Set("Allow", "GET, HEAD, POST")
		refuse(w, http.StatusMethodNotAllowed, "the gateway passes on GET, HEAD and POST requests only, not "+r.Method)
		return
	}
	id, rest, ok := split(r.URL.EscapedPath())
	if !ok {
		refuse(w, http.StatusBadRequest, "not a path the gateway passes on")
		return
	}
	route, ok := p.Routes()[id]
	if !ok {
		refuse(w, http.StatusNotFound, "this sandbox has no provider '"+id+"' behind its gateway")
		return
	}
	token, start := p.Token(), time.Now()
	status := 0
	rp := &httputil.ReverseProxy{
		Transport:     p.Transport,
		FlushInterval: -1, // answers are streamed
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := *route.Base
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawPath = strings.TrimRight(u.EscapedPath(), "/")
			if rest != "" {
				plain, _ := url.PathUnescape(rest)
				u.Path += "/" + plain
				u.RawPath += "/" + rest
			}
			u.RawQuery = r.URL.RawQuery
			if q := route.Base.RawQuery; q != "" {
				u.RawQuery = strings.TrimPrefix(q+"&"+r.URL.RawQuery, "&")
				u.RawQuery = strings.TrimSuffix(u.RawQuery, "&")
			}
			pr.Out.URL = &u
			pr.Out.Host = u.Host
			restore(pr.Out.Header, route, token)
		},
		ModifyResponse: func(resp *http.Response) error {
			status = resp.StatusCode
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status = http.StatusBadGateway
			p.logf("%s: %v", id, err)
			refuse(w, http.StatusBadGateway, "can't reach provider '"+id+"' from your machine: "+err.Error())
		},
	}
	if rp.Transport == nil {
		rp.Transport = DefaultTransport()
	}
	rp.ServeHTTP(w, r)
	p.logf("%s %s /%s -> %d in %s", id, r.Method, rest, status, time.Since(start).Round(time.Millisecond))
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
