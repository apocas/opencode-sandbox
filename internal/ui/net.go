package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/apocas/opencode-sandbox/internal/backend/apple"
	"github.com/apocas/opencode-sandbox/internal/netpolicy"
)

// Plural is "1 host" or "3 hosts".
func Plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// NetRule is a rule as it's shown: with the ports it covers, where it names
// none itself.
func NetRule(rule string) string {
	if r, err := netpolicy.ParseRule(rule); err != nil || r.Port() != 0 {
		return rule
	}
	return rule + " (ports 443, 80 and 22)"
}

// NetWord describes what's on a sandbox's list: a rule, or a built-in list
// with what it's for.
func NetWord(word string) string {
	for _, p := range netpolicy.Presets() {
		if p.Name == word {
			return word + ", a built-in list: " + p.About + " (" + Plural(len(p.Hosts), "host") + ")"
		}
	}
	r, err := netpolicy.ParseRule(word)
	if err != nil {
		return word
	}
	return NetRule(r.String())
}

// HostPort writes a host and a port as a rule would: without the port where
// it's one a rule covers anyway.
func HostPort(host string, port int) string { return netpolicy.Target(host, port) }

// Ago says how long ago something was: "2 minutes ago", "just now".
func Ago(t time.Time) string {
	d := time.Since(t)
	if d < 2*time.Second {
		return "just now"
	}
	return strings.ToLower(strings.TrimPrefix(apple.HumanDuration(d), "About ")) + " ago"
}

// Refused says how often and when a host was refused, and why: "3 times, 2
// minutes ago: it's on no list".
func Refused(b netpolicy.Blocked) string {
	times := "once"
	if b.Count > 1 {
		times = fmt.Sprintf("%d times", b.Count)
	}
	return times + ", " + Ago(b.Last) + ": " + b.Why
}
