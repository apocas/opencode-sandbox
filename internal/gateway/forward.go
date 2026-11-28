package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Filter is the way out of a sandbox with a filtered network. Such a
// sandbox has no network of its own; its programs are pointed at the
// gateway's port as their proxy, and what they ask for there comes here:
// a tunnel to a host (CONNECT, as for HTTPS and ssh) or a plain HTTP
// request. Each is let through only to a host the sandbox may reach.
type Filter struct {
	// Decide says whether the sandbox may reach a host (a name in lower
	// case, or an address) on a port, and why not. It's asked before the
	// name is looked up: a name that isn't allowed is never sent to a
	// resolver.
	Decide func(ctx context.Context, host string, port int) (ok bool, why string)
	// AllowsAddress says whether a rule that names addresses allows this
	// one. Addresses that aren't on the internet (this machine, your
	// network, a cloud's metadata) are reached only that way, also when an
	// allowed name leads there. nil allows none.
	AllowsAddress func(a netip.Addr, port int) bool
	// Refused is told of a connection that Decide allowed and was refused
	// all the same, and why; may be nil.
	Refused func(host string, port int, why string)
	// Log is told about each connection; may be nil.
	Log func(format string, args ...any)

	// dial replaces the dialer in tests.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func (f *Filter) logf(format string, args ...any) {
	if f.Log != nil {
		f.Log(format, args...)
	}
}

func (f *Filter) refused(host string, port int, why string) {
	if f.Refused != nil {
		f.Refused(host, port, why)
	}
}

// connect reaches an address the way the filter does: straight from this
// machine (never through a proxy of its own: the host asked for is the one
// reached), and only where guard lets it.
func (f *Filter) connect(ctx context.Context, network, address string) (net.Conn, error) {
	if f.dial != nil {
		return f.dial(ctx, network, address)
	}
	d := net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: guard(f.AllowsAddress)}
	return d.DialContext(ctx, network, address)
}

// deny answers a request that isn't let through, in words for whoever
// reads the terminal.
func deny(w http.ResponseWriter, code int, msg string) {
	body := "opencode-sandbox: " + msg + "\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// target takes "host:port" apart; the host comes back in lower case,
// without the brackets of an IPv6 address or a final dot. A host is an
// address or a name of letters, digits, dots, hyphens and underscores, and
// nothing else: it's the sandbox's word, and it's shown to you (in a
// dialog, in listings), so it mustn't be able to look like anything but a
// host.
func target(hostport string, usual int) (host string, port int, err error) {
	host, p, splitErr := net.SplitHostPort(hostport)
	if splitErr != nil {
		host, p = hostport, strconv.Itoa(usual)
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	port, err = strconv.Atoi(p)
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if err != nil || port < 1 || port > 65535 || !hostLike(host) {
		return "", 0, fmt.Errorf("%q isn't a host and a port", hostport)
	}
	return host, port, nil
}

// hostLike says whether s could be a host: an address (without a zone), or
// a name.
func hostLike(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Zone() == ""
	}
	if s == "" || len(s) > 253 || s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return !strings.Contains(s, "..")
}

// failed says why a connection failed, to the sandbox: the address isn't
// on the internet, or the host can't be reached.
func (f *Filter) failed(w http.ResponseWriter, host string, port int, err error) {
	var inner ErrInner
	if errors.As(err, &inner) {
		why := fmt.Sprintf("%s leads to %s, which isn't on the internet (it's this machine, or on your network); only a rule that names the address allows that", host, inner.Addr)
		if a, perr := netip.ParseAddr(host); perr == nil {
			why = fmt.Sprintf("%s isn't on the internet (it's this machine, or on your network); only a rule that names the address allows that", a)
		}
		f.refused(host, port, why)
		f.logf("refused %s:%d: %s", host, port, why)
		deny(w, http.StatusForbidden, why)
		return
	}
	f.logf("%s:%d: %v", host, port, err)
	deny(w, http.StatusBadGateway, fmt.Sprintf("can't reach %s:%d from your machine: %v", host, port, err))
}

func (f *Filter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.tunnel(w, r)
		return
	}
	f.plain(w, r)
}

// tunnel serves CONNECT: a connection to a host, for the sandbox to speak
// on as it likes (TLS, ssh).
func (f *Filter) tunnel(w http.ResponseWriter, r *http.Request) {
	host, port, err := target(r.Host, 443)
	if err != nil {
		deny(w, http.StatusBadRequest, err.Error())
		return
	}
	if port == 80 {
		// Plain HTTP is asked for as a request, where the host it's for can
		// be seen; a tunnel would hide it.
		deny(w, http.StatusForbidden, "plain HTTP goes through the proxy as a request, not as a tunnel")
		return
	}
	if ok, why := f.Decide(r.Context(), host, port); !ok {
		f.logf("refused %s:%d: %s", host, port, why)
		deny(w, http.StatusForbidden, why)
		return
	}
	started := time.Now()
	up, err := f.connect(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		f.failed(w, host, port, err)
		return
	}
	defer up.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		deny(w, http.StatusInternalServerError, "can't make a tunnel here")
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer down.Close()
	if _, err := io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	// What the server says goes back at once; what the sandbox says is
	// looked at first, if it starts a TLS handshake.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(down, up)
		done <- struct{}{}
	}()
	go func() {
		if err := f.forward(up, io.MultiReader(io.LimitReader(buf.Reader, int64(buf.Reader.Buffered())), down), host); err != nil {
			f.refused(host, port, err.Error())
			f.logf("closed %s:%d: %v", host, port, err)
		}
		done <- struct{}{}
	}()
	<-done
	f.logf("%s:%d open for %s", host, port, time.Since(started).Round(time.Millisecond))
}

// forward passes what the sandbox sends on to the server. If it starts
// with a TLS handshake, the name it asks for must be the host the tunnel is
// to: a server that hosts many sites would otherwise serve another one.
func (f *Filter) forward(up io.Writer, from io.Reader, host string) error {
	var first []byte
	buf := make([]byte, 16<<10)
	for {
		n, err := from.Read(buf)
		first = append(first, buf[:n]...)
		state, name := helloName(first)
		if state == helloMore && err == nil && len(first) <= maxHello+1024 {
			continue
		}
		switch {
		case state == helloBad || state == helloMore && err == nil:
			return fmt.Errorf("the tunnel to %s was closed: %w", host, errHello)
		case state == helloNamed && name != "" && !strings.EqualFold(strings.TrimSuffix(name, "."), host):
			return fmt.Errorf("the tunnel to %s was closed: its TLS handshake asked for %s instead", host, name)
		}
		if _, werr := up.Write(first); werr != nil || err != nil {
			return nil
		}
		break
	}
	_, _ = io.Copy(up, from)
	return nil
}

// plain serves a plain HTTP request for a page elsewhere (GET http://…).
func (f *Filter) plain(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" {
		deny(w, http.StatusBadRequest, "the proxy passes on http:// requests, and tunnels (CONNECT) for the rest")
		return
	}
	host, port, err := target(r.URL.Host, 80)
	if err != nil {
		deny(w, http.StatusBadRequest, err.Error())
		return
	}
	if ok, why := f.Decide(r.Context(), host, port); !ok {
		f.logf("refused %s:%d: %s", host, port, why)
		deny(w, http.StatusForbidden, why)
		return
	}
	started, status := time.Now(), 0
	rp := &httputil.ReverseProxy{
		FlushInterval: -1,
		Transport: &http.Transport{
			Proxy:                 nil, // straight from this machine
			DialContext:           f.connect,
			DisableCompression:    true,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 5 * time.Minute,
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", r.URL.Host
			pr.Out.Host = r.URL.Host // the host asked for, not another one named in a header
		},
		ModifyResponse: func(resp *http.Response) error {
			status = resp.StatusCode
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status = http.StatusBadGateway
			f.failed(w, host, port, err)
		},
	}
	rp.ServeHTTP(w, r)
	f.logf("%s:%d %s -> %d in %s", host, port, r.Method, status, time.Since(started).Round(time.Millisecond))
}

// Handler serves a sandbox's gateway: its providers and MCP servers
// (proxy), and, where its network is filtered, the hosts it may reach
// (filter; nil where it isn't). A request is for the filter when it's what
// a proxy is asked: CONNECT, or a whole address.
func Handler(proxy http.Handler, filter http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked := r.Method == http.MethodConnect || r.URL.IsAbs()
		if asked && r.Method != http.MethodConnect && ours(r.URL.Host) {
			// The gateway itself, asked for through itself (a program that
			// doesn't leave 127.0.0.1 out of its proxy): the same thing.
			r.URL.Scheme, r.URL.Host = "", ""
			asked = false
		}
		switch {
		case !asked:
			proxy.ServeHTTP(w, r)
		case filter == nil:
			deny(w, http.StatusForbidden, "this sandbox's network isn't filtered, so nothing here is a proxy")
		default:
			filter.ServeHTTP(w, r)
		}
	})
}

// ours says whether a host and port are the gateway's own, inside a sandbox.
func ours(hostport string) bool {
	host, port, err := target(hostport, 80)
	return err == nil && port == Port && (host == "127.0.0.1" || host == "localhost" || host == "::1")
}
