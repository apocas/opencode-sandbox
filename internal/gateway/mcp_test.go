package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/occonfig"
)

func TestMCP(t *testing.T) {
	env := Env{Getenv: func(k string) string {
		return map[string]string{"MCP_TOKEN": "from-env", "HOST": "mcp.example"}[k]
	}}
	root := parse(t, `{"mcp":{
		"timeout": {"startup": 5000},
		"old":      {"type":"remote","url":"https://old.example/mcp","headers":{"Authorization":"Bearer old-secret","empty":""}},
		"servers": {
			"new":      {"type":"remote","url":"https://{env:HOST}/mcp","headers":{"x-api-key":"{env:MCP_TOKEN}"}},
			"query":    {"type":"remote","url":"https://q.example/sse?api_key=in-the-address"},
			"password": {"type":"remote","url":"https://me:pw@p.example/mcp"},
			"open":     {"type":"remote","url":"https://open.example/mcp"},
			"signin":   {"type":"remote","url":"https://s.example/mcp","oauth":{"clientId":"id","clientSecret":"cs"}},
			"both":     {"type":"remote","url":"https://b.example/mcp","headers":{"X-Key":"k"},"oauth":{"clientSecret":"cs"}},
			"badurl":   {"type":"remote","url":"ws://x.example/mcp","headers":{"X-Key":"k"}},
			"nourl":    {"type":"remote","headers":{"X-Key":"k"}},
			"odd":      {"type":"remote","url":"relative/path"},
			"off":      {"type":"remote","url":"https://off.example/mcp","headers":{"X-Key":"k"},"disabled":true},
			"local":    {"type":"local","command":["npx","server"],"environment":{"GITHUB_TOKEN":"ghp_x","DEBUG":"1","API_KEY":"{env:NOPE}","DB_PASSWORD":"pw"}},
			"plain":    {"type":"local","command":["server"],"environment":{"DEBUG":"1"}},
			"bare":     {"type":"local","command":["server"]},
			"old":      {"type":"remote","url":"https://shadowed.example/mcp"},
			"what":     {"type":"carrier-pigeon","url":"https://w.example"},
			"text":     "not a server"
		}
	}}`)
	routes, inside := MCP(root, env, false)
	want := map[string]struct {
		base    string
		headers map[string]string
	}{
		"old":      {"https://shadowed.example/mcp", nil}, // mcp.servers comes first
		"new":      {"https://mcp.example/mcp", map[string]string{"X-Api-Key": "from-env"}},
		"query":    {"https://q.example/sse?api_key=in-the-address", nil},
		"password": {"https://me:pw@p.example/mcp", nil},
		"both":     {"https://b.example/mcp", map[string]string{"X-Key": "k"}},
		"off":      {"https://off.example/mcp", map[string]string{"X-Key": "k"}},
	}
	// "old" under mcp.servers has nothing to keep, and shadows the one with
	// a header: it's served by neither.
	delete(want, "old")
	if len(routes) != len(want) {
		t.Errorf("%d servers behind the gateway, want %d: %v", len(routes), len(want), routes)
	}
	for name, w := range want {
		r, ok := routes[name]
		if !ok || r.Base.String() != w.base || r.Key != "" || len(r.Headers) != len(w.headers) {
			t.Errorf("%s: %v %+v, want %+v", name, ok, r, w)
			continue
		}
		for k, v := range w.headers {
			if r.Headers[k] != v {
				t.Errorf("%s: header %s is %q, want %q", name, k, r.Headers[k], v)
			}
		}
	}
	for name, why := range map[string]string{
		"signin": "not its oauth.clientSecret",
		"both":   "not its oauth.clientSecret",
		"badurl": "its address (ws://x.example/mcp) isn't an http or https URL",
		"nourl":  "its address () isn't an http or https URL",
		"local":  "it runs inside the sandbox, with DB_PASSWORD, GITHUB_TOKEN in its environment",
	} {
		if !strings.Contains(inside[name], why) {
			t.Errorf("%s: %q, want %q", name, inside[name], why)
		}
	}
	if len(inside) != 5 {
		t.Errorf("secrets that go in: %v", inside)
	}

	// Without a network, every remote server with an address is served.
	routes, _ = MCP(root, env, true)
	for _, name := range []string{"old", "new", "query", "password", "open", "signin", "both", "off"} {
		if _, ok := routes[name]; !ok {
			t.Errorf("without a network, %s isn't served", name)
		}
	}
	if len(routes) != 8 {
		t.Errorf("without a network: %v", routes)
	}

	// Servers as older configs have them, and configs with none.
	routes, inside = MCP(parse(t, `{"mcp":{"a":{"type":"remote","url":"https://a.example","headers":{"K":"v"}},
		"servers":{"type":"local","command":["x"],"environment":{"MY_SECRET":"s"}}}}`), env, false)
	if len(routes) != 1 || routes["a"].Headers["K"] != "v" || !strings.Contains(inside["servers"], "MY_SECRET") {
		t.Errorf("an older config: %v %v", routes, inside)
	}
	for _, cfg := range []string{`{}`, `{"mcp":null}`, `{"mcp":"x"}`, `{"mcp":{"servers":[]}}`, `[]`} {
		if routes, inside := MCP(parse(t, cfg), env, true); len(routes)+len(inside) != 0 {
			t.Errorf("%s: %v %v", cfg, routes, inside)
		}
	}
}

func TestRewriteMCP(t *testing.T) {
	const src = `{"mcp":{
		"timeout": {"startup": 5000},
		"old": {"type":"remote","url":"https://old.example/mcp","headers":{"Authorization":"Bearer old-secret","X-Plain":""},"enabled":true},
		"servers": {
			"new one": {"type":"remote","url":"https://new.example/mcp?key=in-the-address","headers":{"x-api-key":"new-secret"},"timeout":{"startup":1}},
			"open":    {"type":"remote","url":"https://open.example/mcp"},
			"local":   {"type":"local","command":["x"],"environment":{"TOKEN":"t"}}
		}
	},"model":"a/b"}`
	root := parse(t, src)
	routes, _ := MCP(root, Env{}, false)
	cfg := root.(*occonfig.Object).Clone()
	RewriteMCP(cfg, routes, token)
	out := string(occonfig.Encode(cfg))
	for _, want := range []string{
		`"url": "http://127.0.0.1:17872/mcp/old"`, `"Authorization": "` + token + `"`, `"X-Plain": ""`, `"enabled": true`,
		`"url": "http://127.0.0.1:17872/mcp/new%20one"`, `"x-api-key": "` + token + `"`, `"startup": 1`,
		`"url": "https://open.example/mcp"`, `"TOKEN": "t"`, `"model": "a/b"`, `"startup": 5000`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the sandbox's config lacks %s:\n%s", want, out)
		}
	}
	for _, secret := range []string{"old-secret", "new-secret", "in-the-address", "old.example", "new.example"} {
		if strings.Contains(out, secret) {
			t.Errorf("the sandbox's config has %q:\n%s", secret, out)
		}
	}
	// Your own config is left as it was.
	if again := string(occonfig.Encode(root)); !strings.Contains(again, "old-secret") || !strings.Contains(again, "new.example/mcp?key=in-the-address") || strings.Contains(again, token) {
		t.Errorf("the config it was made from changed:\n%s", again)
	}
	// Nothing to do: nothing changes.
	cfg = parse(t, `{"model":"a/b"}`).(*occonfig.Object)
	RewriteMCP(cfg, routes, token)
	if got := string(occonfig.Encode(cfg)); strings.Contains(got, "mcp") {
		t.Errorf("a config without servers: %s", got)
	}
}

// mcpProxy is a gateway with one provider (p1, also reachable as "mcp" to
// show the two can't be taken for each other) and one MCP server (tools).
func mcpProxy(t *testing.T, host, at string, r Route) (*Proxy, *httptest.Server) {
	t.Helper()
	r.Base, _ = url.Parse(host + at)
	prov := Route{Key: "REAL"}
	prov.Base, _ = url.Parse(host + "/v1")
	p := &Proxy{Token: func() string { return token }, Routes: func() Table {
		return Table{Providers: map[string]Route{"p1": prov, "mcp": prov, "tools": prov}, MCP: map[string]Route{"tools": r}}
	}}
	gw := httptest.NewServer(p)
	t.Cleanup(gw.Close)
	return p, gw
}

func TestProxyKinds(t *testing.T) {
	up, last := provider(t)
	_, gw := mcpProxy(t, up.URL, "/server/mcp?key=in-the-address", Route{Headers: map[string]string{"Authorization": "Bearer mcp-secret", "X-Other": "o"}})
	do := func(method, path string, header ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, gw.URL+path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, c := range []struct {
		method, path string
		path2, query string // what the server gets
		auth         string // and in Authorization, having been sent the token
	}{
		{"POST", "/mcp/tools", "/server/mcp", "key=in-the-address", "Bearer mcp-secret"},
		{"GET", "/mcp/tools?x=1", "/server/mcp", "key=in-the-address&x=1", "Bearer mcp-secret"},
		{"DELETE", "/mcp/tools", "/server/mcp", "key=in-the-address", "Bearer mcp-secret"},
		{"POST", "/mcp/tools/below", "/server/mcp/below", "key=in-the-address", "Bearer mcp-secret"},
		// A provider, by its kind; one that's named like a kind; and one
		// named like an MCP server.
		{"POST", "/provider/p1/chat", "/v1/chat", "", "REAL"},
		{"POST", "/provider/mcp/chat", "/v1/chat", "", "REAL"},
		{"GET", "/provider/tools/models", "/v1/models", "", "REAL"},
		// As a sandbox's config had it before 0.13.0.
		{"POST", "/p1/chat", "/v1/chat", "", "REAL"},
	} {
		code, body := do(c.method, c.path, "Authorization", token)
		got := last()
		if code != 200 || got.Method != c.method || got.Path != c.path2 || got.Query != c.query || got.Header.Get("Authorization") != c.auth {
			t.Errorf("%s %s: %d %q; the server got %s %s?%s with %q", c.method, c.path, code, body, got.Method, got.Path, got.Query, got.Header.Get("Authorization"))
		}
	}
	// A provider's key is its own: the token after a scheme is the key for a
	// provider, and nothing for an MCP server.
	if do("POST", "/provider/p1/chat", "Authorization", "Bearer "+token); last().Header.Get("Authorization") != "Bearer REAL" {
		t.Errorf("a provider's key: %q", last().Header.Get("Authorization"))
	}
	if do("POST", "/mcp/tools", "Authorization", "Bearer "+token, "X-Other", token, "X-Debug", token); last().Header.Get("Authorization") != "Bearer "+token ||
		last().Header.Get("X-Other") != "o" || last().Header.Get("X-Debug") != token {
		t.Errorf("an MCP server's headers: %v", last().Header)
	}
	for _, c := range []struct {
		method, path string
		code         int
		want         string
	}{
		{"DELETE", "/provider/p1/x", 405, "GET, HEAD and POST requests only"},
		{"DELETE", "/p1/x", 405, "GET, HEAD and POST requests only"},
		{"PUT", "/mcp/tools", 405, "GET, HEAD, POST and DELETE requests only"},
		{"TRACE", "/mcp/tools", 405, "not TRACE"},
		{"GET", "/mcp/p1", 404, "no MCP server 'p1'"},
		{"GET", "/provider/nope/x", 404, "no provider 'nope'"},
		{"GET", "/mcp/tools/../../admin", 400, "not a path"},
		{"GET", "/provider", 404, "no provider 'provider'"}, // a kind with nothing after it names nothing
	} {
		before := last()
		code, body := do(c.method, c.path, "Authorization", token)
		if code != c.code || !strings.Contains(body, c.want) || strings.Contains(body, "mcp-secret") {
			t.Errorf("%s %s: %d %q, want %d %q", c.method, c.path, code, body, c.code, c.want)
		}
		if last().Path != before.Path || last().Method != before.Method {
			t.Errorf("%s %s reached the server", c.method, c.path)
		}
	}
}

// What an MCP server's address has stays here: its path to the letter, a
// password (sent as the server expects it), and a secret in its query, which
// no answer takes back into the sandbox.
func TestProxyMCPAddress(t *testing.T) {
	var got seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = seen{r.Method, r.Host, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), ""}
		if r.URL.Query().Get("go") != "" {
			w.Header().Set("Location", "http://"+r.Host+r.URL.RequestURI()+"&again=1")
			w.Header().Set("Content-Location", r.URL.RequestURI())
			w.WriteHeader(http.StatusTemporaryRedirect)
		}
	}))
	defer up.Close()
	host := strings.TrimPrefix(up.URL, "http://")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ask := func(at, path string) *http.Response {
		t.Helper()
		_, gw := mcpProxy(t, "http://"+at, "", Route{})
		resp, err := client.Post(gw.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// A trailing slash is the server's to have or not.
	for at, want := range map[string]string{host + "/mcp/": "/mcp/", host + "/mcp": "/mcp", host: "", host + "/": "/"} {
		if ask(at, "/mcp/tools"); got.Path != want && !(want == "" && got.Path == "/") {
			t.Errorf("a server at %s was asked at %q", at, got.Path)
		}
	}
	// A password in the address goes the way the server expects it.
	if ask("me:pw@"+host+"/mcp", "/mcp/tools"); got.Header.Get("Authorization") != "Basic bWU6cHc=" {
		t.Errorf("a password in the address: Authorization %q", got.Header.Get("Authorization"))
	}
	// A redirect that repeats the address doesn't take its secret inside.
	resp := ask(host+"/mcp?key=in-the-address", "/mcp/tools?go=1")
	if got.Query != "key=in-the-address&go=1" {
		t.Errorf("the server was asked with %q", got.Query)
	}
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "" || resp.Header.Get("Content-Location") != "" {
		t.Errorf("a redirect from an address with a secret: %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Without a secret in the address, a redirect is the client's to see.
	if resp := ask(host+"/mcp", "/mcp/tools?go=1"); resp.Header.Get("Location") == "" {
		t.Error("a redirect from an address without a secret lost its Location")
	}
	// A server that can't be reached: the answer says so without the address's secret.
	_, gw := mcpProxy(t, "http://127.0.0.1:1", "/mcp?key=in-the-address", Route{})
	r2, err := http.Post(gw.URL+"/mcp/tools", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "can't reach MCP server 'tools'") || strings.Contains(string(body), "in-the-address") {
		t.Errorf("an unreachable server: %d %s", r2.StatusCode, body)
	}
}
