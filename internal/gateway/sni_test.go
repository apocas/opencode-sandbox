package gateway

import (
	"bytes"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

// hello is what a TLS client sends first, asking for a server by name ("" for
// none).
func hello(t *testing.T, name string, more func(*tls.Config)) []byte {
	t.Helper()
	client, server := net.Pipe()
	cfg := &tls.Config{ServerName: name, InsecureSkipVerify: name == ""}
	if more != nil {
		more(cfg)
	}
	go func() { _ = tls.Client(client, cfg).Handshake() }()
	var got []byte
	buf := make([]byte, 32<<10)
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		n, err := server.Read(buf)
		got = append(got, buf[:n]...)
		if st, _ := helloName(got); st != helloMore || err != nil {
			break
		}
	}
	server.Close()
	client.Close()
	return got
}

func TestHelloName(t *testing.T) {
	for _, name := range []string{"example.com", "api.github.com", strings.Repeat("a", 60) + ".example", ""} {
		h := hello(t, name, nil)
		if st, got := helloName(h); st != helloNamed || got != name {
			t.Errorf("a hello for %q: state %d, name %q", name, st, got)
		}
		// However little of it has come, it's never taken for something else.
		for i := 0; i < len(h); i++ {
			if st, _ := helloName(h[:i]); st != helloMore {
				t.Errorf("the first %d of %d bytes of a hello for %q: state %d", i, len(h), name, st)
				break
			}
		}
		// What follows it changes nothing.
		if st, got := helloName(append(append([]byte{}, h...), "more"...)); st != helloNamed || got != name {
			t.Errorf("a hello for %q with more after it: state %d, name %q", name, st, got)
		}
	}
	// Older and narrower clients.
	for what, more := range map[string]func(*tls.Config){
		"TLS 1.2 only": func(c *tls.Config) { c.MaxVersion = tls.VersionTLS12 },
		"with ALPN":    func(c *tls.Config) { c.NextProtos = []string{"h2", "http/1.1"} },
	} {
		if st, got := helloName(hello(t, "old.example", more)); st != helloNamed || got != "old.example" {
			t.Errorf("%s: state %d, name %q", what, st, got)
		}
	}
	// A hello spread over several records is still one hello.
	h := hello(t, "split.example", nil)
	body := h[5:]
	var split []byte
	for len(body) > 0 {
		n := min(len(body), 100)
		split = append(split, 22, 3, 1, byte(n>>8), byte(n))
		split = append(split, body[:n]...)
		body = body[n:]
	}
	if st, got := helloName(split); st != helloNamed || got != "split.example" {
		t.Errorf("a hello in pieces: state %d, name %q", st, got)
	}

	for what, data := range map[string][]byte{
		"ssh":              []byte("SSH-2.0-OpenSSH_10.0\r\n"),
		"http":             []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
		"one other byte":   {0},
		"an old SSL hello": {0x80, 0x2e, 1, 3, 1},
	} {
		if st, _ := helloName(data); st != helloNotTLS {
			t.Errorf("%s: state %d, want it not taken for TLS", what, st)
		}
	}
	if st, _ := helloName(nil); st != helloMore {
		t.Errorf("nothing yet: state %d", st)
	}
	// What starts like a handshake and can't be read is refused, not let by.
	good := hello(t, "example.com", nil)
	at := bytes.Index(good, []byte("example.com"))
	for what, data := range map[string][]byte{
		"a record of nothing":      {22, 3, 1, 0, 0},
		"a record too long":        {22, 3, 1, 0xff, 0xff, 1},
		"not a ClientHello":        {22, 3, 3, 0, 4, 2, 0, 0, 0},
		"a hello too long":         {22, 3, 1, 0, 4, 1, 0xff, 0xff, 0xff},
		"a hello cut short inside": append(append([]byte{}, good[:5]...), append([]byte{1, 0, 0, 40}, good[9:49]...)...),
		"then something else":      append(append([]byte{}, good[:20]...), 23, 3, 3, 0, 1, 0),
	} {
		if what == "a hello cut short inside" {
			data[3], data[4] = 0, 44
		}
		if what == "then something else" {
			data[3], data[4] = 0, 15
		}
		if st, _ := helloName(data); st != helloBad {
			t.Errorf("%s: state %d, want it refused", what, st)
		}
	}
	// Hellos made by hand: one name, none, and what no server should be sent.
	nameExt := func(n string) []byte {
		l := len(n)
		return append([]byte{0, 0, byte((l + 5) >> 8), byte(l + 5), byte((l + 3) >> 8), byte(l + 3), 0, byte(l >> 8), byte(l)}, n...)
	}
	other := []byte{0, 43, 0, 3, 2, 3, 4} // supported_versions
	raw := func(exts ...[]byte) []byte {
		body := append(make([]byte, 0, 256), 3, 3)
		body = append(body, make([]byte, 32)...)       // random
		body = append(body, 0, 0, 2, 0x13, 0x01, 1, 0) // no session, one suite, no compression
		all := bytes.Join(exts, nil)
		if exts != nil {
			body = append(append(body, byte(len(all)>>8), byte(len(all))), all...)
		}
		msg := append([]byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
		return append([]byte{22, 3, 1, byte(len(msg) >> 8), byte(len(msg))}, msg...)
	}
	for what, c := range map[string]struct {
		data  []byte
		state int
		name  string
	}{
		"one name":               {raw(other, nameExt("hand.example")), helloNamed, "hand.example"},
		"no name":                {raw(other), helloNamed, ""},
		"no extensions at all":   {raw(), helloNamed, ""},
		"two names":              {raw(nameExt("a.example"), nameExt("b.example")), helloBad, ""},
		"an empty name":          {raw(nameExt("")), helloBad, ""},
		"a name of another kind": {raw(func() []byte { e := nameExt("a.example"); e[6] = 1; return e }()), helloBad, ""},
		"two entries in a name": {raw(func() []byte {
			e := nameExt("a.example")
			e = append(e, 0, 0, 1, 'b')
			e[3] += 4 // the extension is longer, its list isn't
			return e
		}()), helloBad, ""},
		"an extension cut short": {raw([]byte{0, 0, 0, 9, 0}), helloBad, ""},
	} {
		if st, got := helloName(c.data); st != c.state || got != c.name {
			t.Errorf("%s: state %d, name %q; want %d, %q", what, st, got, c.state, c.name)
		}
	}
	// A name in the middle of a hello that lies about its lengths.
	bad := append([]byte{}, good...)
	bad[at-1]++ // the name's length, one more than there is
	if st, _ := helloName(bad); st != helloBad {
		t.Errorf("a name longer than its extension: state %d", st)
	}
}
