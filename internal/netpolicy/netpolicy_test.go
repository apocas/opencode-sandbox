package netpolicy

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseRule(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":           "example.com",
		"Example.COM.":          "example.com",
		" api.example.com ":     "api.example.com",
		"*.example.com":         "*.example.com",
		"*.Example.co.uk":       "*.example.co.uk",
		"example.com:8443":      "example.com:8443",
		"*.example.com:8443":    "*.example.com:8443",
		"localhost":             "localhost",
		"my_host.internal":      "my_host.internal",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"192.0.2.7":             "192.0.2.7",
		"192.0.2.7:5432":        "192.0.2.7:5432",
		"10.0.0.0/8":            "10.0.0.0/8",
		"10.1.2.3/8":            "10.0.0.0/8",
		"2001:db8::7":           "2001:db8::7",
		"[2001:db8::7]":         "2001:db8::7",
		"[2001:db8::7]:5432":    "[2001:db8::7]:5432",
		"fd00::/8":              "fd00::/8",
		"::ffff:192.0.2.7":      "192.0.2.7",
		"127.0.0.1:8080":        "127.0.0.1:8080",
	} {
		r, err := ParseRule(in)
		if err != nil || r.String() != want {
			t.Errorf("%q: %q, %v; want %q", in, r.String(), err, want)
			continue
		}
		// What's written is read back the same.
		if again, err := ParseRule(r.String()); err != nil || again != r {
			t.Errorf("%q written as %q is read back as %+v, %v", in, r, again, err)
		}
	}
	for in, why := range map[string]string{
		"":                                   "it's empty",
		"*":                                  "only at the start",
		"*.com":                              "most of the internet",
		"*.*.example.com":                    "only at the start",
		"exam*ple.com":                       "only at the start",
		"api.*.example.com":                  "only at the start",
		"https://example.com":                "a rule is a host, not an address of a page",
		"example.com/path":                   "isn't a range of addresses",
		"user@example.com":                   "a rule is a host",
		"example.com:":                       "isn't a port",
		"example.com:0":                      "isn't a port",
		"example.com:65536":                  "isn't a port",
		"example.com:http":                   "isn't a port",
		"example.com:080":                    "isn't a port",
		"bücher.example":                     "written in ASCII",
		"-bad.example":                       "isn't a host name",
		"a..b":                               "isn't a host name",
		"two words":                          "a rule is a host",
		"10.0.0.0/33":                        "isn't a range of addresses",
		"[2001:db8::7":                       "has no ]",
		"[example.com]:443":                  "isn't an IPv6 address",
		"[2001:db8::7]443":                   "after ] comes :PORT",
		"2001:db8::7:x:y:z:w:v:u":            "isn't a host, an address or a range",
		strings.Repeat("a", 64) + ".example": "isn't a host name",
	} {
		if r, err := ParseRule(in); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: %q, %v; want it refused because %q", in, r, err, why)
		}
	}
}

func TestMatches(t *testing.T) {
	for _, c := range []struct {
		rule, host string
		port       int
		want       bool
	}{
		{"example.com", "example.com", 443, true},
		{"example.com", "EXAMPLE.com.", 80, true},
		{"example.com", "example.com", 22, true},
		{"example.com", "example.com", 8443, false},
		{"example.com", "www.example.com", 443, false},
		{"example.com", "notexample.com", 443, false},
		{"example.com:8443", "example.com", 8443, true},
		{"example.com:8443", "example.com", 443, false},
		{"*.example.com", "api.example.com", 443, true},
		{"*.example.com", "a.b.example.com", 443, true},
		{"*.example.com", "example.com", 443, false},
		{"*.example.com", "badexample.com", 443, false},
		{"*.example.com", "example.com.evil.test", 443, false},
		{"192.0.2.7", "192.0.2.7", 443, true},
		{"192.0.2.7", "192.0.2.8", 443, false},
		{"192.0.2.7", "::ffff:192.0.2.7", 443, true},
		{"192.0.2.7:5432", "192.0.2.7", 5432, true},
		{"192.0.2.7:5432", "192.0.2.7", 443, false},
		{"10.0.0.0/8", "10.200.3.4", 22, true},
		{"10.0.0.0/8", "11.0.0.1", 22, false},
		{"10.0.0.0/8", "ten.example", 443, false}, // a name is never an address
		{"example.com", "93.184.216.34", 443, false},
		{"fd00::/8", "fd12::1", 443, true},
		{"[2001:db8::7]:5432", "2001:db8::7", 5432, true},
	} {
		r, err := ParseRule(c.rule)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Matches(c.host, c.port); got != c.want {
			t.Errorf("%s about %s:%d: %v, want %v", c.rule, c.host, c.port, got, c.want)
		}
	}
}

func TestPolicy(t *testing.T) {
	var p Policy
	for _, w := range []string{"github", "example.com", "*.corp.example", "10.0.0.0/8", "192.0.2.7:5432", "example.com"} {
		if err := p.AllowWord(w); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []string{"secret.corp.example", "10.9.0.0/16"} {
		if err := p.DenyWord(w); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(p.Presets, p.Allow, p.Deny) != "[github] [example.com *.corp.example 10.0.0.0/8 192.0.2.7:5432] [secret.corp.example 10.9.0.0/16]" {
		t.Errorf("the policy: %+v", p)
	}
	r := p.Rules()
	for _, c := range []struct {
		host string
		port int
		want Verdict
		rule string
	}{
		{"github.com", 443, Allowed, "github.com"},
		{"github.com", 22, Allowed, "github.com"},
		{"ssh.github.com", 443, Allowed, "ssh.github.com:443"},
		{"ssh.github.com", 22, Unlisted, ""},
		{"gist.github.com", 443, Unlisted, ""},
		{"example.com", 80, Allowed, "example.com"},
		{"build.corp.example", 443, Allowed, "*.corp.example"},
		{"secret.corp.example", 443, Denied, "secret.corp.example"}, // deny wins
		{"10.1.2.3", 443, Allowed, "10.0.0.0/8"},
		{"10.9.2.3", 443, Denied, "10.9.0.0/16"},
		{"192.0.2.7", 5432, Allowed, "192.0.2.7:5432"},
		{"192.0.2.7", 443, Unlisted, ""},
		{"elsewhere.test", 443, Unlisted, ""},
	} {
		if got, rule := r.Decide(c.host, c.port); got != c.want || rule != c.rule {
			t.Errorf("%s:%d: %v by %q, want %v by %q", c.host, c.port, got, rule, c.want, c.rule)
		}
	}
	// An address is allowed only by a rule that names addresses.
	for addr, want := range map[string]bool{"10.1.2.3": true, "10.9.2.3": false, "192.168.1.1": false, "::ffff:10.1.2.3": true} {
		if got := r.AllowsAddress(netip.MustParseAddr(addr), 443); got != want {
			t.Errorf("address %s: %v, want %v", addr, got, want)
		}
	}
	if r.AllowsAddress(netip.MustParseAddr("192.0.2.7"), 443) || !r.AllowsAddress(netip.MustParseAddr("192.0.2.7"), 5432) {
		t.Error("an address with a port of its own")
	}

	// Allowing what was denied takes the denial back, and the other way.
	if err := p.AllowWord("secret.corp.example"); err != nil || len(p.Deny) != 1 {
		t.Errorf("allowing a denied host: %v %+v", err, p)
	}
	if err := p.DenyWord("EXAMPLE.com"); err != nil || fmt.Sprint(p.Allow) != "[*.corp.example 10.0.0.0/8 192.0.2.7:5432 secret.corp.example]" || p.Deny[1] != "example.com" {
		t.Errorf("denying an allowed host: %v %+v", err, p)
	}
	if err := p.DenyWord("github"); err != nil || len(p.Presets) != 0 || len(p.Deny) != 2 {
		t.Errorf("taking a built-in list off: %v %+v", err, p)
	}
	for word, why := range map[string]string{"git hub": "neither a host nor one of the built-in lists (github, gitlab, go, npm and pypi)", "*.com": "most of the internet", "a.example b.example": "a rule is a host"} {
		if err := p.AllowWord(word); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("allowing %q: %v, want %q", word, err, why)
		}
		if err := Check(word); err == nil {
			t.Errorf("Check(%q) passed", word)
		}
	}
	if Check("pypi") != nil || Check("example.com:8080") != nil {
		t.Error("Check refused a list or a rule")
	}
	// A file changed by hand: what can't be read is left out, the rest holds.
	odd := Policy{Presets: []string{"nope", "npm"}, Allow: []string{"*", "ok.example"}, Deny: []string{"https://x"}}
	if v, _ := odd.Rules().Decide("registry.npmjs.org", 443); v != Allowed {
		t.Error("a policy with an unknown list lost the known one")
	}
	if v, _ := odd.Rules().Decide("ok.example", 443); v != Allowed {
		t.Error("a policy with a bad rule lost the good one")
	}
	if v, _ := odd.Rules().Decide("anything.test", 443); v != Unlisted {
		t.Error("a bad rule allowed something")
	}
}

// Every host of every built-in list is a rule, and none is a wildcard.
func TestPresets(t *testing.T) {
	if len(Presets()) != 5 {
		t.Errorf("%d built-in lists", len(Presets()))
	}
	for _, p := range Presets() {
		if p.About == "" || len(p.Hosts) == 0 {
			t.Errorf("%s: %+v", p.Name, p)
		}
		if _, err := ParseRule(p.Name); err == nil && strings.Contains(p.Name, ".") {
			t.Errorf("list %s could be taken for a host", p.Name)
		}
		for _, h := range p.Hosts {
			r, err := ParseRule(h)
			if err != nil || r.String() != h || strings.Contains(h, "*") || r.IsAddress() {
				t.Errorf("%s: %q: %v", p.Name, h, err)
			}
		}
	}
}

func TestStore(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "net", "demo")}
	if p, err := s.Load(); err != nil || len(p.Allow)+len(p.Deny)+len(p.Presets) != 0 {
		t.Errorf("a policy never written: %+v, %v", p, err)
	}
	if list, err := s.Blocked(); err != nil || len(list) != 0 {
		t.Errorf("nothing refused yet: %v, %v", list, err)
	}
	before := s.Stamp()
	if err := s.Update(func(p *Policy) error { return p.AllowWord("npm") }); err != nil {
		t.Fatal(err)
	}
	if s.Stamp() == before {
		t.Error("the stamp didn't change with the policy")
	}
	// A change that fails changes nothing.
	if err := s.Update(func(p *Policy) error { p.Allow = []string{"x.example"}; return fmt.Errorf("no") }); err == nil {
		t.Error("a failed change was kept")
	}
	if p, _ := s.Load(); fmt.Sprint(p.Presets, p.Allow) != "[npm] []" {
		t.Errorf("after a failed change: %+v", p)
	}
	// Several writers at once lose nothing.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Update(func(p *Policy) error { return p.AllowWord(fmt.Sprintf("h%d.example", i)) })
			_ = s.Refused(fmt.Sprintf("b%d.example", i%5), 443, "it's on no list")
		}(i)
	}
	wg.Wait()
	p, _ := s.Load()
	if len(p.Allow) != 20 {
		t.Errorf("%d rules after 20 writers", len(p.Allow))
	}
	list, _ := s.Blocked()
	total := 0
	for _, b := range list {
		total += b.Count
		if b.Port != 443 || b.Why == "" || b.First.IsZero() || b.Last.Before(b.First) {
			t.Errorf("a refused host: %+v", b)
		}
	}
	if len(list) != 5 || total != 20 {
		t.Errorf("%d hosts refused %d times, want 5 and 20", len(list), total)
	}
	// Private: rules say what a sandbox reaches.
	for _, f := range []string{"policy.json", "blocked.json"} {
		if fi, err := os.Stat(filepath.Join(s.Dir, f)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", f, err)
		}
	}
	if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("the folder is %v", fi.Mode().Perm())
	}
	// What's allowed since is taken off the list of refused hosts.
	_ = s.Update(func(p *Policy) error { return p.AllowWord("b3.example") })
	p, _ = s.Load()
	if err := s.Forget(p.Rules()); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Blocked(); len(list) != 4 {
		t.Errorf("%d refused hosts after one was allowed", len(list))
	}
	// A host's usual ports are one line, as one rule allows them; another
	// port is its own.
	for _, port := range []int{443, 80, 22, 8443, 443} {
		_ = s.Refused("ports.example", port, "it's on no list")
	}
	list, _ = s.Blocked()
	if list[0].Host != "ports.example" || list[0].Count != 4 || list[1].Host != "ports.example" || list[1].Port != 8443 || list[1].Count != 1 {
		t.Errorf("a host refused on several ports: %+v %+v", list[0], list[1])
	}
	for host, want := range map[string]string{"example.com:443": "example.com", "example.com:22": "example.com", "example.com:8443": "example.com:8443",
		"192.0.2.7:80": "192.0.2.7", "192.0.2.7:5432": "192.0.2.7:5432", "2001:db8::1:443": "2001:db8::1", "2001:db8::1:8443": "[2001:db8::1]:8443"} {
		i := strings.LastIndex(host, ":")
		var port int
		fmt.Sscan(host[i+1:], &port)
		got := Target(host[:i], port)
		if got != want {
			t.Errorf("Target(%s): %q, want %q", host, got, want)
		}
		// It's the rule that allows exactly that.
		if r, err := ParseRule(got); err != nil || !r.Matches(host[:i], port) {
			t.Errorf("the rule %q doesn't allow %s: %v", got, host, err)
		}
	}
	// Only the latest are kept.
	for i := 0; i < maxBlocked+10; i++ {
		_ = s.Refused(fmt.Sprintf("n%d.example", i), 443, "it's on no list")
	}
	if list, _ := s.Blocked(); len(list) != maxBlocked || list[0].Host != fmt.Sprintf("n%d.example", maxBlocked+9) {
		t.Errorf("%d refused hosts kept, the latest %s", len(list), list[0].Host)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Error("the folder is still there")
	}
}
