package core

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/netpolicy"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// What a sandbox with a filtered network may reach is what its rules say,
// as they are now; what it's refused is written down, with how to allow it.
func TestNetGuard(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var logged []string
	g := newNetGuard("demo", func(f string, a ...any) { logged = append(logged, f) })
	ctx := context.Background()
	for _, c := range []struct {
		host string
		port int
		why  string
	}{
		{"github.com", 443, "github.com isn't on the list of hosts sandbox 'demo' may reach; on your machine, `opencode-sandbox allow demo github.com` adds it"},
		{"github.com", 22, "`opencode-sandbox allow demo github.com` adds it"},
		{"db.example", 5432, "`opencode-sandbox allow demo db.example:5432` adds it"},
		{"2001:db8::1", 8443, "`opencode-sandbox allow demo [2001:db8::1]:8443` adds it"},
		{"192.0.2.7", 80, "`opencode-sandbox allow demo 192.0.2.7` adds it"},
	} {
		ok, why := g.decide(ctx, c.host, c.port)
		if ok || !strings.Contains(why, c.why) {
			t.Errorf("%s:%d: %v %q, want %q", c.host, c.port, ok, why, c.why)
		}
	}
	// A rule is what the message says it is: what it names is then let through.
	store := netStore("demo")
	if store.Dir != state.NetDir("demo") {
		t.Errorf("the rules are kept in %s", store.Dir)
	}
	for _, w := range []string{"github", "db.example:5432", "[2001:db8::1]:8443", "192.0.2.7", "*.corp.example"} {
		if err := store.Update(func(p *netpolicy.Policy) error { return p.AllowWord(w) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		host string
		port int
	}{{"github.com", 443}, {"github.com", 22}, {"db.example", 5432}, {"2001:db8::1", 8443}, {"192.0.2.7", 80}, {"build.corp.example", 443}} {
		if ok, why := g.decide(ctx, c.host, c.port); !ok {
			t.Errorf("%s:%d, allowed since: %s", c.host, c.port, why)
		}
	}
	if !g.allowsAddress(netip.MustParseAddr("192.0.2.7"), 443) || g.allowsAddress(netip.MustParseAddr("192.0.2.8"), 443) {
		t.Error("which addresses the rules name")
	}
	// A denial wins, and says which rule it is.
	_ = store.Update(func(p *netpolicy.Policy) error { return p.DenyWord("secret.corp.example") })
	if ok, why := g.decide(ctx, "secret.corp.example", 443); ok || why != "secret.corp.example is denied to sandbox 'demo' (rule secret.corp.example)" {
		t.Errorf("a denied host: %v %q", ok, why)
	}
	blocked, err := store.Blocked()
	// (github.com's two ports are one line: one rule would allow both.)
	if err != nil || len(blocked) != 5 {
		t.Fatalf("%d refused hosts written down, %v: %+v", len(blocked), err, blocked)
	}
	if b := blocked[0]; b.Host != "secret.corp.example" || b.Port != 443 || b.Why != "a rule denies it (secret.corp.example)" || b.Count != 1 {
		t.Errorf("the latest refused: %+v", b)
	}
	if b := blocked[len(blocked)-1]; b.Host != "github.com" || b.Port != 443 || b.Why != "it's on no list" || b.Count != 2 {
		t.Errorf("the first refused: %+v", b)
	}
	// Rules that can't be read allow nothing, and that's said.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if len(logged) != 0 {
		t.Errorf("logged: %q", logged)
	}
}

// Programs in a sandbox with a filtered network are pointed at the gateway,
// and never for the gateway itself.
func TestProxyEnv(t *testing.T) {
	env := map[string]string{}
	for _, kv := range proxyEnv() {
		if _, twice := env[kv[0]]; twice {
			t.Errorf("%s is set twice", kv[0])
		}
		env[kv[0]] = kv[1]
	}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy", "YARN_HTTP_PROXY", "YARN_HTTPS_PROXY"} {
		if env[k] != "http://127.0.0.1:17872" {
			t.Errorf("%s=%q", k, env[k])
		}
	}
	if env["NO_PROXY"] != "127.0.0.1,localhost" || env["no_proxy"] != env["NO_PROXY"] || env["NODE_USE_ENV_PROXY"] != "1" || env["OPENCODE_SANDBOX_PROXY"] != "17872" {
		t.Errorf("the rest: %v", env)
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
