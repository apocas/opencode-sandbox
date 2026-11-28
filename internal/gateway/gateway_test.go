package gateway

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apocas/opencode-sandbox/internal/occonfig"
)

const token = "opencode-sandbox-0123456789abcdef"

// seen is what the provider got.
type seen struct {
	Method, Host, Path, Query string
	Header                    http.Header
	Body                      string
}

// provider is a stand-in for a provider's API: it records each request and
// answers with its path.
func provider(t *testing.T) (*httptest.Server, func() seen) {
	var mu sync.Mutex
	var last seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = seen{r.Method, r.Host, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), string(body)}
		mu.Unlock()
		if r.URL.Path == "/v1/stream" {
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "data: %d\n\n", i)
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
			return
		}
		fmt.Fprintf(w, "served %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv, func() seen { mu.Lock(); defer mu.Unlock(); return last }
}

func proxyFor(base string, r Route) *Proxy {
	r.Base, _ = url.Parse(base)
	return &Proxy{Token: func() string { return token }, Routes: func() Table { return Table{Providers: map[string]Route{"p1": r}} }}
}

func TestProxyPutsSecretsBack(t *testing.T) {
	up, last := provider(t)
	p := proxyFor(up.URL+"/v1/", Route{Key: "REAL", Headers: map[string]string{"X-Team": "team-secret", "Authorization": "Basic abc"}})
	gw := httptest.NewServer(p)
	defer gw.Close()

	cases := []struct {
		name          string
		header, value string
		want          string // what the provider gets in that header
	}{
		{"bearer", "Authorization", "Bearer " + token, "Bearer REAL"},
		{"another scheme", "Authorization", "Token " + token, "Token REAL"},
		{"the key alone", "x-api-key", token, "REAL"},
		{"google's header", "x-goog-api-key", token, "REAL"},
		{"azure's header", "api-key", token, "REAL"},
		{"a header of the config", "X-Team", token, "team-secret"},
		{"a header of the config wins over the key", "Authorization", token, "Basic abc"},
		// Nothing but the whole credential is the token: a provider that
		// echoes part of a bad key must not get part of the real one.
		{"a prefix", "Authorization", "Bearer x" + token, "Bearer x" + token},
		{"a suffix", "Authorization", "Bearer " + token + "x", "Bearer " + token + "x"},
		{"two words before it", "Authorization", "Bearer extra " + token, "Bearer extra " + token},
		{"a header keys don't travel in", "X-Debug", token, token},
		{"a header keys don't travel in, with a scheme", "X-Debug", "Bearer " + token, "Bearer " + token},
		{"someone else's key", "Authorization", "Bearer other", "Bearer other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", gw.URL+"/p1/chat/completions?stream=true&x="+token, strings.NewReader(`{"say":"`+token+`"}`))
			req.Header.Set(c.header, c.value)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got := last()
			if resp.StatusCode != 200 || string(body) != "served /v1/chat/completions" {
				t.Fatalf("answer: %d %q", resp.StatusCode, body)
			}
			if v := got.Header.Get(c.header); v != c.want {
				t.Errorf("%s: the provider got %q, want %q", c.header, v, c.want)
			}
			// Never in the body or the query: a model would say it back.
			if got.Body != `{"say":"`+token+`"}` || got.Query != "stream=true&x="+token {
				t.Errorf("body %q, query %q", got.Body, got.Query)
			}
			if got.Host != strings.TrimPrefix(up.URL, "http://") {
				t.Errorf("Host %q", got.Host)
			}
			if got.Header.Get("X-Forwarded-For") != "" {
				t.Errorf("X-Forwarded-For: %q", got.Header.Get("X-Forwarded-For"))
			}
		})
	}
}

func TestProxyPaths(t *testing.T) {
	up, last := provider(t)
	p := proxyFor(up.URL+"/v1?api-version=7", Route{Key: "REAL"})
	gw := httptest.NewServer(p)
	defer gw.Close()
	get := func(path string) (int, string) {
		// As it is on the wire: a client that doesn't tidy the path up.
		conn, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: gw\r\nConnection: close\r\n\r\n", path)
		data, _ := io.ReadAll(conn)
		var code int
		fmt.Sscanf(string(data), "HTTP/1.1 %d", &code)
		_, body, _ := strings.Cut(string(data), "\r\n\r\n")
		return code, body
	}
	for path, want := range map[string]string{
		"/p1/models":                 "/v1/models",
		"/p1/models/a%20b:generate":  "/v1/models/a%20b:generate",
		"/p1":                        "/v1",
		"/p1/":                       "/v1",
		"/p1/deep/er/path?alt=sse&k": "/v1/deep/er/path",
	} {
		if code, _ := get(path); code != 200 || last().Path != want {
			t.Errorf("%s: %d, the provider got %s, want %s", path, code, last().Path, want)
		}
	}
	if q := last().Query; q != "api-version=7&alt=sse&k" && q != "api-version=7&k" {
		// (map order: whichever path came last)
		if !strings.HasPrefix(q, "api-version=7") {
			t.Errorf("query %q lost the API's own", q)
		}
	}
	for _, path := range []string{"/p1/../admin", "/p1/%2e%2e/admin", "/p1/a/./b", "/p1/a%2fb", "/p1/a%5cb", "/../p1/x", "/"} {
		if code, body := get(path); code != 400 || !strings.Contains(body, "opencode-sandbox") {
			t.Errorf("%s: %d %q, want it refused", path, code, body)
		}
	}
	if code, _ := get("/p1/%zz"); code != 400 { // net/http refuses it itself
		t.Errorf("/p1/%%zz: %d, want it refused", code)
	}
	if code, body := get("/p2/models"); code != 404 || !strings.Contains(body, "no provider 'p2'") {
		t.Errorf("another provider: %d %q", code, body)
	}
}

// Only what talking to a model takes is passed on. TRACE in particular makes
// a server send the request back, key and all.
func TestProxyMethods(t *testing.T) {
	asked := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		fmt.Fprintf(w, "%s %s", r.Method, r.Header.Get("Authorization")) // an echo, as TRACE is
	}))
	defer up.Close()
	gw := httptest.NewServer(proxyFor(up.URL+"/v1", Route{Key: "REAL"}))
	defer gw.Close()
	do := func(method string) (int, string) {
		req, _ := http.NewRequest(method, gw.URL+"/p1/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, m := range []string{"TRACE", "DELETE", "PUT", "PATCH", "OPTIONS", "CONNECT", "PROPFIND", "get"} {
		code, body := do(m)
		if code != 405 || strings.Contains(body, "REAL") || !strings.Contains(body, "GET, HEAD and POST") {
			t.Errorf("%s: %d %q, want it refused", m, code, body)
		}
	}
	if asked != 0 {
		t.Errorf("the provider was asked %d times", asked)
	}
	for _, m := range []string{"GET", "POST", "HEAD"} {
		if code, body := do(m); code != 200 || m != "HEAD" && body != m+" Bearer REAL" {
			t.Errorf("%s: %d %q", m, code, body)
		}
	}
}

func TestProxyProviderDown(t *testing.T) {
	p := proxyFor("http://127.0.0.1:1/v1", Route{Key: "REAL"})
	gw := httptest.NewServer(p)
	defer gw.Close()
	resp, err := http.Get(gw.URL + "/p1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(body), "can't reach provider 'p1'") || strings.Contains(string(body), "REAL") {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

// inside runs gateway.pl, the half of the gateway that's inside a sandbox,
// and returns a session with it and the port it listens on.
func inside(t *testing.T) (*Session, int) {
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("perl not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(perl, filepath.Join("..", "..", "gateway.pl"), "serve", fmt.Sprint(port))
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := NewSession(out, in)
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait(); s.Close() })
	select {
	case <-s.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("gateway.pl didn't say it's listening")
	}
	return s, port
}

// The whole way: a client inside the sandbox, gateway.pl, the session, the
// proxy and the provider.
func TestThroughTheSandbox(t *testing.T) {
	up, last := provider(t)
	s, port := inside(t)
	srv := &http.Server{Handler: proxyFor(up.URL+"/v1", Route{Key: "REAL"})}
	go func() { _ = srv.Serve(s) }()
	defer srv.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d/p1", port)
	client := &http.Client{Timeout: 30 * time.Second}

	req, _ := http.NewRequest("GET", base+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "served /v1/models" || last().Header.Get("Authorization") != "Bearer REAL" {
		t.Fatalf("%q, the provider got %q", body, last().Header.Get("Authorization"))
	}

	// A streamed answer arrives as it's sent.
	resp, err = client.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 9)
	start := time.Now()
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "data: 0\n\n" {
		t.Fatalf("stream: %q %v", first, err)
	}
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(rest) != "data: 1\n\ndata: 2\n\n" || time.Since(start) < 20*time.Millisecond {
		t.Errorf("stream: rest %q after %s", rest, time.Since(start))
	}

	// Many at once, with bodies larger than a frame, each getting its own.
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			big := make([]byte, 300<<10)
			_, _ = rand.Read(big)
			echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, r.Body) }))
			defer echo.Close()
			_ = echo // (the shared provider below answers with the path)
			resp, err := client.Post(fmt.Sprintf("%s/n/%d", base, i), "application/octet-stream", bytes.NewReader(big))
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != fmt.Sprintf("served /v1/n/%d", i) {
				t.Errorf("request %d got %q", i, body)
			}
		}(i)
	}
	wg.Wait()
}

// A big answer comes through whole.
func TestBigAnswerThroughTheSandbox(t *testing.T) {
	big := make([]byte, 5<<20)
	_, _ = rand.Read(big)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, big) {
			http.Error(w, "the request arrived changed", 500)
			return
		}
		_, _ = w.Write(big)
	}))
	defer up.Close()
	s, port := inside(t)
	srv := &http.Server{Handler: proxyFor(up.URL, Route{Key: "REAL"})}
	go func() { _ = srv.Serve(s) }()
	defer srv.Close()
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/p1/x", port), "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, big) {
		t.Errorf("%d, %d bytes back (want %d, the same)", resp.StatusCode, len(got), len(big))
	}
}

// The session ends when the half inside does, and Accept says so.
func TestSessionEnds(t *testing.T) {
	s, port := inside(t)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c, err := s.Accept()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(conn, "hello")
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("%q %v", buf, err)
	}
	// Closed inside: closed here.
	conn.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(buf); err != io.EOF {
		t.Errorf("after the sandbox closed it: %v, want EOF", err)
	}
	s.Close()
	if _, err := s.Accept(); err == nil {
		t.Error("Accept after Close")
	}
}

// What a sandbox sends is its word only: nonsense ends the session, and
// nothing it sends makes this side fall over.
func TestSessionRefusesNonsense(t *testing.T) {
	frame := func(id uint32, typ byte, n uint32, data string) []byte {
		b := []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id), typ, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		return append(b, data...)
	}
	for name, in := range map[string][]byte{
		"a huge frame":      frame(1, frameData, 1<<30, ""),
		"an unknown type":   frame(1, 9, 0, ""),
		"opened twice":      append(frame(7, frameOpen, 0, ""), frame(7, frameOpen, 0, "")...),
		"cut short":         frame(1, frameData, 10, "abc"),
		"data for no one":   append(frame(5, frameData, 3, "abc"), frame(5, frameClose, 0, "")...),
		"nothing but ready": frame(0, frameReady, 0, ""),
	} {
		t.Run(name, func(t *testing.T) {
			s := NewSession(bytes.NewReader(in), io.Discard)
			go func() {
				for {
					c, err := s.Accept()
					if err != nil {
						return
					}
					c.Close()
				}
			}()
			select {
			case <-s.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the session didn't end")
			}
		})
	}
	// More connections than allowed: the extra ones are closed.
	var in bytes.Buffer
	for i := uint32(1); i <= maxStreams+5; i++ {
		in.Write(frame(i, frameOpen, 0, ""))
	}
	pr, pw := io.Pipe()
	var out bytes.Buffer
	var mu sync.Mutex
	s := NewSession(pr, writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) }))
	held := make(chan net.Conn, maxStreams+5)
	go func() {
		for {
			c, err := s.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()
	_, _ = pw.Write(in.Bytes())
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := bytes.Count(out.Bytes(), frame(maxStreams+1, frameClose, 0, ""))
		mu.Unlock()
		if n == 1 && len(held) == maxStreams || time.Now().After(deadline) {
			if n != 1 {
				t.Errorf("the connection over the limit wasn't closed")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // any accepted beyond the limit would show by now
	if len(held) != maxStreams {
		t.Errorf("%d connections accepted, want %d", len(held), maxStreams)
	}
	s.Close()
	pw.Close()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func parse(t *testing.T, s string) any {
	root, err := occonfig.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home", "k"), []byte("from-home"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := Env{Dir: dir, Home: filepath.Join(dir, "home"), Getenv: func(k string) string {
		return map[string]string{"MY_KEY": "from-env", "HOST": "api.example"}[k]
	}}
	root := parse(t, `{"provider":{
		"plain":     {"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://one.example/v1","apiKey":"k1"}},
		"env":       {"options":{"baseURL":"https://{env:HOST}/v1","apiKey":"{env:MY_KEY}"}},
		"unset":     {"options":{"baseURL":"https://x.example","apiKey":"{env:NOPE}"}},
		"file":      {"options":{"baseURL":"https://x.example","apiKey":"{file:key.txt}"}},
		"home":      {"options":{"baseURL":"https://x.example","apiKey":"{file:~/k}"}},
		"headers":   {"options":{"baseURL":"http://lan.example:8080","headers":{"x-team":"t1","Authorization":"Bearer h","empty":"","n":5}}},
		"api":       {"api":"https://api.example/v2","options":{"apiKey":"k"}},
		"anthropic": {"options":{"apiKey":"sk-ant"}},
		"mine":      {"npm":"@ai-sdk/openai","options":{"apiKey":"sk-oai"}},
		"unknown":   {"npm":"@ai-sdk/some-other","options":{"apiKey":"k"}},
		"nowhere":   {"options":{"apiKey":"k"}},
		"badurl":    {"options":{"baseURL":"ftp://x.example","apiKey":"k"}},
		"relative":  {"options":{"baseURL":"/v1","apiKey":"k"}},
		"keyless":   {"options":{"baseURL":"http://localhost:11434/v1"}},
		"odd":       "not an object",
		"oddopts":   {"options":"not an object"},
		"aws":       {"npm":"@ai-sdk/amazon-bedrock","options":{"region":"eu-west-1","accessKeyId":"AKIAEXAMPLE","secretAccessKey":"S1","sessionToken":"S2"}},
		"both":      {"options":{"baseURL":"https://x.example","apiKey":"k","clientSecret":"S3","nested":{"private_key":"S4","list":[{"password":"S5"}]},"one":"1","more":{"token":"S6"}}},
		"models":    {"options":{"baseURL":"https://x.example","apiKey":"k"},"models":{"m1":{"headers":{"Authorization":"Bearer S7","anthropic-beta":"x"}},"m2":{"options":{"apiKey":"S8"}},"m3":"odd"}},
		"pointers":  {"options":{"baseURL":"https://x.example","apiKey":"k","keyFile":"/k.json","apiKeyEnv":"MY_KEY","accessKeyId":"AKIA","tokenUrl":"https://t.example","publicKey":"pk","maxTokens":5,"secretAccessKey":"{env:AWS_SECRET}","empty_token":""}}
	}}`)
	ids := []string{"plain", "env", "unset", "file", "home", "headers", "api", "anthropic", "mine", "unknown", "nowhere", "badurl", "relative", "keyless", "odd", "oddopts", "missing", "aws", "both", "models", "pointers"}
	routes, skipped := Routes(root, ids, env, false)

	want := map[string]struct{ base, key string }{
		"plain":     {"https://one.example/v1", "k1"},
		"env":       {"https://api.example/v1", "from-env"},
		"file":      {"https://x.example", "from-file"},
		"home":      {"https://x.example", "from-home"},
		"headers":   {"http://lan.example:8080", ""},
		"api":       {"https://api.example/v2", "k"},
		"anthropic": {"https://api.anthropic.com/v1", "sk-ant"},
		"mine":      {"https://api.openai.com/v1", "sk-oai"},
		"both":      {"https://x.example", "k"},
		"models":    {"https://x.example", "k"},
		"pointers":  {"https://x.example", "k"},
	}
	if len(routes) != len(want) {
		t.Errorf("routes for %d providers, want %d: %v", len(routes), len(want), routes)
	}
	for id, w := range want {
		r, ok := routes[id]
		if !ok || r.Base.String() != w.base || r.Key != w.key {
			t.Errorf("%s: %v %+v, want %+v", id, ok, r, w)
		}
	}
	if h := routes["headers"].Headers; len(h) != 2 || h["X-Team"] != "t1" || h["Authorization"] != "Bearer h" {
		t.Errorf("headers: %v", h)
	}
	for id, why := range map[string]string{"unknown": "where its API is", "nowhere": "where its API is", "badurl": "isn't an http or https URL", "relative": "isn't an http or https URL",
		// Secrets that aren't the API key or a header: the gateway doesn't
		// keep them, whether it serves the provider or not.
		"aws":    "the gateway keeps a provider's options.apiKey and options.headers, not its options.secretAccessKey, options.sessionToken",
		"both":   "not its options.clientSecret, options.nested.private_key, options.nested.list.password and 1 more",
		"models": "not its models.m1.headers.Authorization, models.m2.options.apiKey",
	} {
		if !strings.Contains(skipped[id], why) {
			t.Errorf("%s: a secret inside because %q, want %q", id, skipped[id], why)
		}
	}
	// Nothing to keep out of the sandbox: neither served nor complained about.
	for _, id := range []string{"unset", "keyless", "odd", "oddopts", "missing"} {
		if _, ok := routes[id]; ok || skipped[id] != "" {
			t.Errorf("%s: served %v, skipped %q", id, ok, skipped[id])
		}
	}
	// What only points at a secret (a file, an id, a variable), isn't one
	// (maxTokens), or isn't there inside a sandbox ({env:…}) is no secret.
	if skipped["pointers"] != "" {
		t.Errorf("pointers: %q", skipped["pointers"])
	}
	if len(skipped) != 7 {
		t.Errorf("secrets inside: %v", skipped)
	}
	if r, s := Routes(parse(t, `[1]`), ids, env, false); len(r)+len(s) != 0 {
		t.Errorf("a config that isn't an object: %v %v", r, s)
	}
}

// A sandbox without a network reaches every provider through the gateway:
// one without secrets is served too, as long as its API's address is known.
func TestRoutesForNoNetwork(t *testing.T) {
	root := parse(t, `{"provider":{
		"keyed":   {"options":{"baseURL":"https://one.example/v1","apiKey":"SECRET"}},
		"keyless": {"options":{"baseURL":"http://localhost:11434/v1"}},
		"builtin": {"npm":"@ai-sdk/anthropic"},
		"nowhere": {"models":{"m":{}}},
		"aws":     {"options":{"secretAccessKey":"S"}}}}`)
	ids := []string{"keyed", "keyless", "builtin", "nowhere", "aws"}
	routes, inside := Routes(root, ids, Env{}, true)
	for id, base := range map[string]string{"keyed": "https://one.example/v1", "keyless": "http://localhost:11434/v1", "builtin": "https://api.anthropic.com/v1"} {
		if r, ok := routes[id]; !ok || r.Base.String() != base {
			t.Errorf("%s: served %v at %v, want %s", id, ok, r.Base, base)
		}
	}
	if len(routes) != 3 || routes["keyless"].Key != "" || routes["keyed"].Key != "SECRET" {
		t.Errorf("routes: %v", routes)
	}
	// Not served: no address. Only one of them has a secret to speak of.
	if len(inside) != 1 || !strings.Contains(inside["aws"], "options.secretAccessKey") {
		t.Errorf("secrets inside: %v", inside)
	}
	// Its config points each at the gateway; one without a key gets none.
	cfg, err := occonfig.Filter(root, ids)
	if err != nil {
		t.Fatal(err)
	}
	Rewrite(cfg, routes, token)
	got := string(occonfig.Encode(cfg))
	for _, want := range []string{`"baseURL": "http://127.0.0.1:17872/provider/keyed"`, `"baseURL": "http://127.0.0.1:17872/provider/keyless"`, `"baseURL": "http://127.0.0.1:17872/provider/builtin"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the sandbox's config lacks %s:\n%s", want, got)
		}
	}
	if strings.Count(got, token) != 1 || strings.Contains(got, "localhost:11434") {
		t.Errorf("the sandbox's config:\n%s", got)
	}
	// With a network, the ones without secrets are left alone.
	if routes, _ := Routes(root, ids, Env{}, false); len(routes) != 1 {
		t.Errorf("with a network: %v", routes)
	}
}

func TestRewrite(t *testing.T) {
	root := parse(t, `{"model":"a/m","provider":{
		"a":{"name":"A","npm":"@ai-sdk/openai-compatible","models":{"m":{}},"options":{"timeout":5,"baseURL":"https://one.example/v1","apiKey":"SECRET-A","headers":{"X-Team":"SECRET-T","empty":""}}},
		"b c":{"options":{"apiKey":"SECRET-B"},"api":"https://b.example"},
		"keyless":{"options":{"baseURL":"http://host.docker.internal:11434/v1"}},
		"nowhere":{"options":{"apiKey":"STAYS"}}}}`)
	ids := []string{"a", "b c", "keyless", "nowhere"}
	cfg, err := occonfig.Filter(root, ids)
	if err != nil {
		t.Fatal(err)
	}
	routes, _ := Routes(root, ids, Env{}, false)
	Rewrite(cfg, routes, token)
	got := string(occonfig.Encode(cfg))
	for _, secret := range []string{"SECRET-A", "SECRET-T", "SECRET-B", "one.example", "b.example"} {
		if strings.Contains(got, secret) {
			t.Errorf("the sandbox's config has %s:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		`"baseURL": "http://127.0.0.1:17872/provider/a"`, `"baseURL": "http://127.0.0.1:17872/provider/b%20c"`,
		`"apiKey": "` + token + `"`, `"X-Team": "` + token + `"`, `"empty": ""`, `"timeout": 5`, `"name": "A"`,
		// Left as they were: one has nothing secret, the other no known API.
		`"baseURL": "http://host.docker.internal:11434/v1"`, `"apiKey": "STAYS"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the sandbox's config lacks %s:\n%s", want, got)
		}
	}
	// Your own config is as it was.
	if before := string(occonfig.Encode(root)); !strings.Contains(before, "SECRET-A") || strings.Contains(before, token) {
		t.Errorf("the original changed:\n%s", before)
	}
}
