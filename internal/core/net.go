package core

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"sync"

	"github.com/apocas/opencode-sandbox/internal/gateway"
	"github.com/apocas/opencode-sandbox/internal/netpolicy"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// proxyEnv is what points the programs of a sandbox with a filtered network
// at the gateway as their proxy: the variables most of them read, in both
// cases, and the ones Node and Yarn want besides. OPENCODE_SANDBOX_PROXY is
// for ssh (see ssh_config), which reads none of them.
func proxyEnv() [][2]string {
	at := "http://127.0.0.1:" + strconv.Itoa(gateway.Port)
	const direct = "127.0.0.1,localhost"
	return [][2]string{
		{"HTTPS_PROXY", at}, {"HTTP_PROXY", at}, {"ALL_PROXY", at}, {"NO_PROXY", direct},
		{"https_proxy", at}, {"http_proxy", at}, {"all_proxy", at}, {"no_proxy", direct},
		{"NODE_USE_ENV_PROXY", "1"}, {"YARN_HTTP_PROXY", at}, {"YARN_HTTPS_PROXY", at},
		{"OPENCODE_SANDBOX_PROXY", strconv.Itoa(gateway.Port)},
	}
}

// netStore is where a sandbox's rules are kept.
func netStore(name string) netpolicy.Store { return netpolicy.Store{Dir: state.NetDir(name)} }

// filtered says why a sandbox has no rules to show or change: it isn't
// there, or its network isn't filtered. nil if it has.
func (a *Core) filtered(name string) error {
	e := a.engineOf(name)
	if e == nil {
		return noSuch(name)
	}
	network, err := e.Label(state.Prefix+name, state.Label("network"))
	if err != nil {
		return err
	}
	if network != "filtered" {
		if network == "" {
			network = "open"
		}
		return fmt.Errorf("sandbox '%s' has no list of hosts: its network is %s. A filtered network is chosen when a sandbox is made: opencode-sandbox new --network filtered", name, network)
	}
	return nil
}

// Net is what a sandbox with a filtered network may reach, and what it was
// refused.
type Net struct {
	Policy  netpolicy.Policy
	Blocked []netpolicy.Blocked
}

// Net reads a sandbox's rules and the hosts it was refused.
func (a *Core) Net(name string) (Net, error) {
	if err := a.filtered(name); err != nil {
		return Net{}, err
	}
	return ReadNet(name)
}

// ReadNet reads the rules kept under a sandbox's name and the hosts it was
// refused, without asking its engine whether its network is filtered: for
// a sandbox that's known to have one.
func ReadNet(name string) (Net, error) {
	s := netStore(name)
	p, err := s.Load()
	if err != nil {
		return Net{}, err
	}
	b, err := s.Blocked()
	return Net{Policy: p, Blocked: b}, err
}

// Allow lets a sandbox reach hosts (rules, or built-in lists of them), at
// once if it's open: its gateway reads the rules again when they change.
func (a *Core) Allow(name string, words ...string) error {
	return a.changeNet(name, words, (*netpolicy.Policy).AllowWord)
}

// Deny refuses a sandbox hosts, whatever else allows them; a built-in list
// is taken off instead.
func (a *Core) Deny(name string, words ...string) error {
	return a.changeNet(name, words, (*netpolicy.Policy).DenyWord)
}

func (a *Core) changeNet(name string, words []string, change func(*netpolicy.Policy, string) error) error {
	if err := a.filtered(name); err != nil {
		return err
	}
	s := netStore(name)
	var after netpolicy.Policy
	err := s.Update(func(p *netpolicy.Policy) error {
		for _, w := range words {
			if err := change(p, w); err != nil {
				return err
			}
		}
		after = *p
		return nil
	})
	if err != nil {
		return err
	}
	return s.Forget(after.Rules())
}

// netGuard decides what a sandbox with a filtered network may reach, for
// its gateway: by its rules, read again when they change, and for a host
// on no list by asking, where there's someone to ask.
type netGuard struct {
	name  string
	store netpolicy.Store
	logf  func(string, ...any)
	// ask asks about a host that's on no list; nil where nobody is asked.
	ask *asker

	mu    sync.Mutex
	rules *netpolicy.Rules
	stamp string
	// once are the hosts allowed until the sandbox closes, as a rule would
	// name them: this gateway ends with it.
	once map[string]bool
}

func newNetGuard(name string, logf func(string, ...any)) *netGuard {
	return &netGuard{name: name, store: netStore(name), logf: logf, ask: newAsker(name), once: map[string]bool{}}
}

func (g *netGuard) allowedOnce(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.once[key]
}

// current is the sandbox's rules as they are now.
func (g *netGuard) current() *netpolicy.Rules {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.store.Stamp(); g.rules == nil || s != g.stamp {
		p, err := g.store.Load()
		if err != nil {
			g.logf("can't read the sandbox's rules, so nothing is allowed: %v", err)
		}
		g.rules, g.stamp = p.Rules(), s
	}
	return g.rules
}

// target writes a host and a port as the rule that would allow them.
func target(host string, port int) string { return netpolicy.Target(host, port) }

// unasked are the hosts opencode itself tries at every start (its list of
// models). Nobody is asked about them on screen: a question at every start
// of every sandbox teaches only to answer without reading. They're refused
// and written down like any host on no list, and can be allowed like any.
var unasked = map[string]bool{"models.opencode.ai": true}

// decide says whether the sandbox may reach a host, and why not; what's
// refused is written down for `opencode-sandbox net`.
func (g *netGuard) decide(ctx context.Context, host string, port int) (bool, string) {
	verdict, rule := g.current().Decide(host, port)
	key := target(host, port)
	if verdict == netpolicy.Unlisted && g.allowedOnce(key) {
		return true, ""
	}
	// Nobody is asked about what could never be on a list.
	_, ruleErr := netpolicy.ParseRule(key)
	if verdict == netpolicy.Unlisted && g.ask != nil && !unasked[host] && ruleErr == nil {
		switch g.ask.about(ctx, key) {
		case answerAlways:
			err := g.store.Update(func(p *netpolicy.Policy) error { return p.AllowWord(key) })
			if err == nil {
				g.logf("%s: allowed for good, on screen", key)
				return true, ""
			}
			g.logf("can't keep that %s is allowed: %v", key, err)
		case answerOnce:
			g.mu.Lock()
			g.once[key] = true
			g.mu.Unlock()
			g.logf("%s: allowed until the sandbox closes, on screen", key)
			return true, ""
		case answerDeny:
			// Kept, so that a host tried at every start is asked about once.
			if err := g.store.Update(func(p *netpolicy.Policy) error { return p.DenyWord(key) }); err != nil {
				g.logf("can't keep that %s is denied: %v", key, err)
			}
			g.logf("%s: denied, on screen", key)
			verdict, rule = netpolicy.Denied, key
		}
	}
	why := ""
	switch verdict {
	case netpolicy.Allowed:
		return true, ""
	case netpolicy.Denied:
		why = fmt.Sprintf("%s is denied to sandbox '%s' (rule %s)", target(host, port), g.name, rule)
	default:
		why = fmt.Sprintf("%s isn't on the list of hosts sandbox '%s' may reach; on your machine, `opencode-sandbox allow %s %s` adds it",
			target(host, port), g.name, g.name, target(host, port))
	}
	g.refused(host, port, map[netpolicy.Verdict]string{netpolicy.Denied: "a rule denies it (" + rule + ")", netpolicy.Unlisted: "it's on no list"}[verdict])
	return false, why
}

func (g *netGuard) refused(host string, port int, why string) {
	if err := g.store.Refused(host, port, why); err != nil {
		g.logf("can't write down that %s was refused: %v", target(host, port), err)
	}
}

func (g *netGuard) allowsAddress(a netip.Addr, port int) bool {
	return g.current().AllowsAddress(a, port) || g.allowedOnce(target(a.Unmap().WithZone("").String(), port))
}

// filter is the way out of the sandbox, by its rules.
func (g *netGuard) filter() *gateway.Filter {
	return &gateway.Filter{Decide: g.decide, AllowsAddress: g.allowsAddress, Refused: g.refused, Log: g.logf}
}
