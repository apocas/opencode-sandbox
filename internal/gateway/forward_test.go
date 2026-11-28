package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// filtered is a filter that allows the hosts in allow ("name:port"), with
// names leading where lead says (a test's servers are on this machine), and
// a record of what it was asked, where it connected and what it refused.
type filtered struct {
	*Filter
	mu                     sync.Mutex
	asked, dialed, refused []string
}

func newFiltered(allow map[string]bool, lead map[string]string) *filtered {
	ft := &filtered{}
	ft.Filter = &Filter{
		Decide: func(_ context.Context, host string, port int) (bool, string) {
			hp := fmt.Sprintf("%s:%d", host, port)
			ft.mu.Lock()
			ft.asked = append(ft.asked, hp)
			ft.mu.Unlock()
			if allow[hp] {
				return true, ""
			}
			return false, hp + " isn't on this sandbox's list"
		},
		Refused: func(host string, port int, why string) {
			ft.mu.Lock()
			ft.refused = append(ft.refused, fmt.Sprintf("%s:%d %s", host, port, why))
			ft.mu.Unlock()
		},
	}
	if lead != nil {
		ft.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			ft.mu.Lock()
			ft.dialed = append(ft.dialed, address)
			ft.mu.Unlock()
			to, ok := lead[address]
			if !ok {
				return nil, fmt.Errorf("no such host: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, to)
		}
	}
	return ft
}

func (ft *filtered) seen() (asked, dialed, refused []string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]string(nil), ft.asked...), append([]string(nil), ft.dialed...), append([]string(nil), ft.refused...)
}

// connectTo asks a proxy for a tunnel, as a client does, and returns the
// connection once the tunnel is there, or the proxy's answer.
func connectTo(t *testing.T, proxy, hostport string) (net.Conn, int, string) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(proxy, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", hostport, hostport)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", hostport, err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		c.Close()
		return nil, resp.StatusCode, string(body)
	}
	if br.Buffered() != 0 {
		t.Fatalf("CONNECT %s: %d bytes after the answer", hostport, br.Buffered())
	}
	return c, 200, ""
}

func hostOf(u string) string { return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://") }

func TestFilterTunnels(t *testing.T) {
	var gotSNI []string
	var mu sync.Mutex
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s%s", r.Host, r.URL.Path)
	}))
	up.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		gotSNI = append(gotSNI, h.ServerName)
		mu.Unlock()
		return nil, nil
	}}
	up.StartTLS()
	defer up.Close()
	ft := newFiltered(map[string]bool{"allowed.test:443": true, "allowed.test:8443": true, "gone.test:443": true},
		map[string]string{"allowed.test:443": hostOf(up.URL), "allowed.test:8443": hostOf(up.URL)})
	gw := httptest.NewServer(ft)
	defer gw.Close()
	proxy, _ := url.Parse(gw.URL)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	defer client.CloseIdleConnections()

	// A host on the list: a tunnel, and TLS inside it that the filter doesn't read.
	for _, u := range []string{"https://allowed.test/a", "https://allowed.test:8443/b", "https://ALLOWED.test./c"} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "hello from ") {
			t.Errorf("%s: %d %q", u, resp.StatusCode, body)
		}
		client.CloseIdleConnections()
	}
	// A host that isn't: no tunnel, and its name goes to no resolver.
	if _, err := client.Get("https://other.test/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("a host off the list: %v", err)
	}
	if _, code, body := connectTo(t, gw.URL, "other.test:443"); code != 403 || body != "opencode-sandbox: other.test:443 isn't on this sandbox's list\n" {
		t.Errorf("a host off the list: %d %q", code, body)
	}
	// A host that can't be reached says so.
	if _, code, body := connectTo(t, gw.URL, "gone.test:443"); code != 502 || !strings.Contains(body, "can't reach gone.test:443 from your machine") {
		t.Errorf("a host that isn't there: %d %q", code, body)
	}
	// Never a tunnel for plain HTTP, or to something that isn't a host.
	for hp, want := range map[string]int{"allowed.test:80": 403, "allowed.test:0": 400, "allowed.test:99999": 400, "al lowed.test:443": 400, ":443": 400,
		"allowed.test@evil.test:443": 403, // the host is the part after the @: one that's on no list
		"allowed.test%00:443":        400, "all\x1b[2Jowed.test:443": 400, "b\u00fccher.test:443": 400,
		"[fe80::1%25eth0]:443": 400, "..test:443": 400, "-x.test:443": 400, "a..b:443": 400} {
		c, err := net.Dial("tcp", hostOf(gw.URL))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: x\r\n\r\n", hp)
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if !strings.Contains(line, fmt.Sprint(" ", want, " ")) {
			t.Errorf("CONNECT %s: %q, want %d", hp, strings.TrimSpace(line), want)
		}
	}
	asked, dialed, _ := ft.seen()
	for _, a := range asked {
		if strings.Contains(a, "ALLOWED") || strings.HasSuffix(strings.Split(a, ":")[0], ".") {
			t.Errorf("asked about %q: a host is asked about in lower case, without a final dot", a)
		}
	}
	for _, d := range dialed {
		if strings.Contains(d, "other.test") {
			t.Errorf("a host off the list was looked for: %v", dialed)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotSNI) != 3 {
		t.Errorf("the server was asked for %q", gotSNI)
	}
}

// The name in a TLS handshake is the host the tunnel is to, or the tunnel
// is closed before the server hears of it.
func TestFilterTLSName(t *testing.T) {
	var heard bytes.Buffer
	var mu sync.Mutex
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					mu.Lock()
					heard.Write(buf[:n])
					mu.Unlock()
					if n > 0 {
						_, _ = c.Write([]byte("heard"))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	ft := newFiltered(map[string]bool{"allowed.test:443": true, "allowed.test:2222": true, "192.0.2.9:443": true},
		map[string]string{"allowed.test:443": l.Addr().String(), "allowed.test:2222": l.Addr().String(), "192.0.2.9:443": l.Addr().String()})
	gw := httptest.NewServer(ft)
	defer gw.Close()
	say := func(hostport string, data []byte) string {
		t.Helper()
		c, code, body := connectTo(t, gw.URL, hostport)
		if code != 200 {
			t.Fatalf("CONNECT %s: %d %s", hostport, code, body)
		}
		defer c.Close()
		// In two pieces, as a network may deliver it.
		_, _ = c.Write(data[:len(data)/2])
		time.Sleep(20 * time.Millisecond)
		_, _ = c.Write(data[len(data)/2:])
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, _ := io.ReadAll(io.LimitReader(c, 5))
		return string(got)
	}
	for what, c := range map[string]struct {
		to   string
		data []byte
		ok   bool
	}{
		"the host's own name":            {"allowed.test:443", hello(t, "allowed.test", nil), true},
		"its name in another case":       {"allowed.test:443", hello(t, "ALLOWED.test", nil), true},
		"no name at all":                 {"allowed.test:443", hello(t, "", nil), true},
		"an address, and no name":        {"192.0.2.9:443", hello(t, "", nil), true},
		"not TLS at all (ssh)":           {"allowed.test:2222", []byte("SSH-2.0-OpenSSH_10.0\r\n"), true},
		"another host's name":            {"allowed.test:443", hello(t, "other.test", nil), false},
		"another name, on another port":  {"allowed.test:2222", hello(t, "other.test", nil), false},
		"a name, to an address":          {"192.0.2.9:443", hello(t, "other.test", nil), false},
		"a handshake that can't be read": {"allowed.test:443", []byte{22, 3, 1, 0, 4, 2, 0, 0, 0}, false},
	} {
		mu.Lock()
		heard.Reset()
		mu.Unlock()
		got := say(c.to, c.data)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		reached := heard.Len()
		mu.Unlock()
		if c.ok && (got != "heard" || reached != len(c.data)) {
			t.Errorf("%s: answered %q, the server heard %d of %d bytes", what, got, reached, len(c.data))
		}
		if !c.ok && (got != "" || reached != 0) {
			t.Errorf("%s: answered %q, and the server heard %d bytes", what, got, reached)
		}
	}
	_, _, refused := ft.seen()
	if len(refused) != 4 || !strings.Contains(strings.Join(refused, "\n"), "allowed.test:443 the tunnel to allowed.test was closed: its TLS handshake asked for other.test instead") {
		t.Errorf("what was refused: %q", refused)
	}
}

func TestFilterPlainHTTP(t *testing.T) {
	up, last := provider(t)
	ft := newFiltered(map[string]bool{"allowed.test:80": true, "allowed.test:8080": true}, map[string]string{"allowed.test:80": hostOf(up.URL), "allowed.test:8080": hostOf(up.URL)})
	gw := httptest.NewServer(ft)
	defer gw.Close()
	proxy, _ := url.Parse(gw.URL)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest("POST", "http://allowed.test/v1/thing?x=1", strings.NewReader("body"))
	req.Header.Set("Proxy-Authorization", "Basic abc")
	req.Header.Set("X-Mine", "kept")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := last()
	if resp.StatusCode != 200 || string(body) != "served /v1/thing" || got.Method != "POST" || got.Host != "allowed.test" || got.Path != "/v1/thing" || got.Query != "x=1" || got.Body != "body" {
		t.Errorf("%d %q; the server got %+v", resp.StatusCode, body, got)
	}
	if got.Header.Get("X-Mine") != "kept" || got.Header.Get("Proxy-Authorization") != "" || got.Header.Get("X-Forwarded-For") != "" {
		t.Errorf("headers the server got: %v", got.Header)
	}
	if resp, err := client.Get("http://allowed.test:8080/v1/x"); err != nil || resp.StatusCode != 200 || last().Host != "allowed.test:8080" {
		t.Errorf("another port: %v, Host %q", err, last().Host)
	}
	// A host header that names another host changes nothing: the address does.
	raw, _ := net.Dial("tcp", hostOf(gw.URL))
	fmt.Fprint(raw, "GET http://allowed.test/v1/named HTTP/1.1\r\nHost: other.test\r\nConnection: close\r\n\r\n")
	answer, _ := io.ReadAll(raw)
	raw.Close()
	if !strings.HasPrefix(string(answer), "HTTP/1.1 200 ") || last().Host != "allowed.test" || last().Path != "/v1/named" {
		t.Errorf("a Host of its own: %q, the server got Host %q", strings.SplitN(string(answer), "\r\n", 2)[0], last().Host)
	}
	resp, err = client.Get("http://other.test/secret")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || string(body) != "opencode-sandbox: other.test:80 isn't on this sandbox's list\n" {
		t.Errorf("a host off the list: %d %q", resp.StatusCode, body)
	}
	// Only http:// is a request; the rest is a tunnel's.
	c, _ := net.Dial("tcp", hostOf(gw.URL))
	fmt.Fprint(c, "GET ftp://allowed.test/x HTTP/1.1\r\nHost: allowed.test\r\n\r\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.Contains(line, " 400 ") {
		t.Errorf("ftp through the proxy: %q", line)
	}
}

// An address that isn't on the internet is reached only by a rule that names
// it; an allowed name that leads there isn't enough.
func TestFilterInnerAddresses(t *testing.T) {
	up, _ := provider(t)
	_, port, _ := net.SplitHostPort(hostOf(up.URL))
	named := false
	ft := newFiltered(map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}, nil)
	ft.AllowsAddress = func(a netip.Addr, p int) bool { return named && a.String() == "127.0.0.1" && fmt.Sprint(p) == port }
	gw := httptest.NewServer(ft)
	defer gw.Close()
	proxy, _ := url.Parse(gw.URL)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	defer client.CloseIdleConnections()
	get := func(host string) (int, string) {
		t.Helper()
		resp, err := client.Get("http://" + host + ":" + port + "/v1/x")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get("127.0.0.1"); code != 403 || !strings.Contains(body, "127.0.0.1 isn't on the internet (it's this machine, or on your network); only a rule that names the address allows that") {
		t.Errorf("this machine, by its address: %d %q", code, body)
	}
	if code, body := get("localhost"); code != 403 || !strings.Contains(body, "localhost leads to ") || !strings.Contains(body, "which isn't on the internet") {
		t.Errorf("this machine, by a name: %d %q", code, body)
	}
	if _, code, body := connectTo(t, gw.URL, "127.0.0.1:"+port); code != 403 || !strings.Contains(body, "isn't on the internet") {
		t.Errorf("a tunnel to this machine: %d %q", code, body)
	}
	_, _, refused := ft.seen()
	if len(refused) != 3 || !strings.HasPrefix(refused[0], "127.0.0.1:"+port+" 127.0.0.1 isn't on the internet") {
		t.Errorf("what was refused: %q", refused)
	}
	// With a rule that names the address, it's reached: by address, and by a
	// name that leads there.
	named = true
	if code, body := get("127.0.0.1"); code != 200 || body != "served /v1/x" {
		t.Errorf("with a rule for the address: %d %q", code, body)
	}
	if c, code, body := connectTo(t, gw.URL, "127.0.0.1:"+port); code != 200 {
		t.Errorf("a tunnel, with a rule for the address: %d %q", code, body)
	} else {
		c.Close()
	}
}

// Handler sends what a proxy is asked to the filter and the rest to the
// gateway's own proxy.
func TestHandler(t *testing.T) {
	to := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s %s %s", name, r.Method, r.URL.String())
		})
	}
	ask := func(h http.Handler, line string) string {
		gw := httptest.NewServer(h)
		defer gw.Close()
		c, err := net.Dial("tcp", hostOf(gw.URL))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "%s HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n", line)
		data, _ := io.ReadAll(c)
		_, body, _ := strings.Cut(string(data), "\r\n\r\n")
		return strings.SplitN(string(data), "\r\n", 2)[0] + " | " + strings.TrimSpace(body)
	}
	both := Handler(to("proxy"), to("filter"))
	for line, want := range map[string]string{
		"GET /provider/p1/models":                           "HTTP/1.1 200 OK | proxy GET /provider/p1/models",
		"POST /mcp/tools":                                   "HTTP/1.1 200 OK | proxy POST /mcp/tools",
		"GET http://example.com/x":                          "HTTP/1.1 200 OK | filter GET http://example.com/x",
		"CONNECT example.com:443":                           "HTTP/1.1 200 OK | filter CONNECT //example.com:443",
		"GET http://127.0.0.1:17872/provider/p1/models?a=1": "HTTP/1.1 200 OK | proxy GET /provider/p1/models?a=1",
		"GET http://localhost:17872/mcp/tools":              "HTTP/1.1 200 OK | proxy GET /mcp/tools",
		"GET http://127.0.0.1:8080/x":                       "HTTP/1.1 200 OK | filter GET http://127.0.0.1:8080/x",
		"CONNECT 127.0.0.1:17872":                           "HTTP/1.1 200 OK | filter CONNECT //127.0.0.1:17872",
	} {
		if got := ask(both, line); got != want {
			t.Errorf("%s: %q, want %q", line, got, want)
		}
	}
	// A sandbox whose network isn't filtered has a gateway and no proxy.
	open := Handler(to("proxy"), nil)
	if got := ask(open, "GET /provider/p1/models"); got != "HTTP/1.1 200 OK | proxy GET /provider/p1/models" {
		t.Errorf("without a filter: %q", got)
	}
	for _, line := range []string{"CONNECT example.com:443", "GET http://example.com/x"} {
		if got := ask(open, line); got != "HTTP/1.1 403 Forbidden | opencode-sandbox: this sandbox's network isn't filtered, so nothing here is a proxy" {
			t.Errorf("without a filter, %s: %q", line, got)
		}
	}
}

// The whole way out for a program that can't use a proxy: gateway.pl's
// connect, through gateway.pl's relay, the session and the filter, to a
// server and back.
func TestConnectThroughTheSandbox(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("SSH-2.0-stand-in\r\n"))
				// Says back the first line it's told, and ends.
				line, _ := bufio.NewReader(c).ReadString('\n')
				_, _ = c.Write([]byte("you said " + line))
			}()
		}
	}()
	ft := newFiltered(map[string]bool{"git.test:22": true}, map[string]string{"git.test:22": l.Addr().String()})
	s, port := inside(t)
	srv := &http.Server{Handler: Handler(http.NotFoundHandler(), ft)}
	go func() { _ = srv.Serve(s) }()
	defer srv.Close()
	run := func(host string, in string) (string, string, error) {
		cmd := exec.Command("perl", filepath.Join("..", "..", "gateway.pl"), "connect", host, "22")
		cmd.Env = append(os.Environ(), fmt.Sprint("OPENCODE_SANDBOX_PROXY=", port))
		cmd.Stdin = strings.NewReader(in)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		return out.String(), errOut.String(), err
	}
	// What's written goes there, and what the server says comes back, also
	// after our side has no more to say.
	out, errOut, err := run("git.test", "SSH-2.0-client\r\n")
	if err != nil || out != "SSH-2.0-stand-in\r\nyou said SSH-2.0-client\r\n" || errOut != "" {
		t.Errorf("an allowed host: %q, %q, %v", out, errOut, err)
	}
	out, errOut, err = run("other.test", "x")
	if err == nil || out != "" || errOut != "opencode-sandbox: other.test:22 isn't on this sandbox's list\n" {
		t.Errorf("a host off the list: %q, %q, %v", out, errOut, err)
	}
}
