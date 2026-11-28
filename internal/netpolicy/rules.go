// Package netpolicy says which hosts a sandbox with a filtered network may
// reach: rules for hosts, built-in lists of them (presets), and the store
// that keeps a sandbox's rules and what was refused.
package netpolicy

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// usual are the ports a rule without a port covers: HTTPS, HTTP and SSH.
var usual = []int{443, 80, 22}

// Rule is one host, or a set of them, and the ports it covers.
type Rule struct {
	text string // as it's written back

	// One of the three says which hosts.
	host   string       // a name, in lower case
	suffix string       // ".example.com", for *.example.com
	net    netip.Prefix // an address or a range of them

	port int // 0 for the usual ports
}

// String is the rule as it's written.
func (r Rule) String() string { return r.text }

// Port is the one port the rule covers; 0 where it covers the usual ones
// (443, 80 and 22).
func (r Rule) Port() int { return r.port }

// IsAddress says whether the rule names addresses, not a name.
func (r Rule) IsAddress() bool { return r.net.IsValid() }

// name checks a host name and returns it in lower case.
func name(s string) (string, error) {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if s == "" || len(s) > 253 {
		return "", fmt.Errorf("it isn't a host name")
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("it isn't a host name")
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			case c > 127:
				return "", fmt.Errorf("names are written in ASCII here (the xn-- form of a name with other letters)")
			default:
				return "", fmt.Errorf("it isn't a host name")
			}
		}
	}
	return s, nil
}

// ParseRule reads a rule: a host (example.com), every host under a domain
// (*.example.com), an address (192.0.2.7, 2001:db8::7) or a range of them
// (10.0.0.0/8), each for ports 443, 80 and 22; or one of those with a port
// of its own (example.com:8443, 192.0.2.7:5432, [2001:db8::7]:5432).
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	bad := func(why string) (Rule, error) { return Rule{}, fmt.Errorf("%q can't be a rule: %s", s, why) }
	if s == "" {
		return bad("it's empty")
	}
	if strings.ContainsAny(s, " \t@?#") || strings.Contains(s, "://") {
		return bad("a rule is a host, not an address of a page: example.com, *.example.com, 192.0.2.7 or 10.0.0.0/8, with :PORT for one port")
	}
	r := Rule{}
	body := s
	// A range of addresses has no port of its own.
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return bad("it isn't a range of addresses like 10.0.0.0/8")
		}
		r.net = p.Masked()
		r.text = r.net.String()
		return r, nil
	}
	// A port: after the last colon of a name or an IPv4 address, or after
	// the brackets of an IPv6 one.
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 {
			return bad("its [ has no ]")
		}
		body = s[1:end]
		if rest := s[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return bad("after ] comes :PORT or nothing")
			}
			p, err := port(rest[1:])
			if err != nil {
				return bad(err.Error())
			}
			r.port = p
		}
		if _, err := netip.ParseAddr(body); err != nil {
			return bad("what's in brackets isn't an IPv6 address")
		}
	case strings.Count(s, ":") == 1:
		h, p, _ := strings.Cut(s, ":")
		n, err := port(p)
		if err != nil {
			return bad(err.Error())
		}
		body, r.port = h, n
	}
	if a, err := netip.ParseAddr(body); err == nil {
		a = a.Unmap().WithZone("")
		r.net = netip.PrefixFrom(a, a.BitLen())
		r.text = a.String()
		if r.port != 0 {
			r.text = net.JoinHostPort(a.String(), strconv.Itoa(r.port))
		}
		return r, nil
	}
	if strings.Contains(body, ":") {
		return bad("it isn't a host, an address or a range of addresses")
	}
	wild := strings.HasPrefix(body, "*.")
	if wild {
		body = body[2:]
	}
	if strings.Contains(body, "*") {
		return bad("a * stands for every host under a domain, and only at the start: *.example.com")
	}
	h, err := name(body)
	if err != nil {
		return bad(err.Error())
	}
	switch {
	case wild && !strings.Contains(h, "."):
		return bad("every host under ." + h + " is most of the internet; name a domain, like *.example." + h)
	case wild:
		r.suffix, r.text = "."+h, "*."+h
	default:
		r.host, r.text = h, h
	}
	if r.port != 0 {
		r.text += ":" + strconv.Itoa(r.port)
	}
	return r, nil
}

func port(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != s {
		return 0, fmt.Errorf("%q isn't a port (1 to 65535)", s)
	}
	return n, nil
}

// Target writes a host (a name or an address) and a port as the rule that
// would allow them: without the port where it's one a rule covers anyway.
func Target(host string, p int) string {
	for _, u := range usual {
		if u == p {
			return host
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(p))
}

// covers says whether the rule covers that port.
func (r Rule) covers(p int) bool {
	if r.port != 0 {
		return r.port == p
	}
	for _, u := range usual {
		if u == p {
			return true
		}
	}
	return false
}

// Matches says whether the rule is about a host (a name in any case, or an
// address) and a port.
func (r Rule) Matches(host string, p int) bool {
	if !r.covers(p) {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return r.net.IsValid() && r.net.Contains(a.Unmap().WithZone(""))
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case r.host != "":
		return host == r.host
	case r.suffix != "":
		return strings.HasSuffix(host, r.suffix) && len(host) > len(r.suffix)
	}
	return false
}

// Verdict is what a sandbox's rules say about a host.
type Verdict int

const (
	// Unlisted: no rule is about it, so it's refused, and may be asked about.
	Unlisted Verdict = iota
	Allowed
	// Denied: a rule refuses it, whatever else allows it.
	Denied
)

// Rules are a sandbox's rules, ready to be asked.
type Rules struct {
	allow, deny []Rule
}

// Decide says what the rules say about a host and a port, and which rule
// said it.
func (r *Rules) Decide(host string, p int) (Verdict, string) {
	for _, d := range r.deny {
		if d.Matches(host, p) {
			return Denied, d.String()
		}
	}
	for _, a := range r.allow {
		if a.Matches(host, p) {
			return Allowed, a.String()
		}
	}
	return Unlisted, ""
}

// AllowsAddress says whether a rule that names addresses allows this one.
// An address that isn't on the internet (this machine, your network) is
// reached only that way: a name that leads there isn't enough.
func (r *Rules) AllowsAddress(a netip.Addr, p int) bool {
	host := a.Unmap().WithZone("").String()
	for _, d := range r.deny {
		if d.Matches(host, p) {
			return false
		}
	}
	for _, rule := range r.allow {
		if rule.IsAddress() && rule.Matches(host, p) {
			return true
		}
	}
	return false
}
