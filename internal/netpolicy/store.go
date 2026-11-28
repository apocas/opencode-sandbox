package netpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// Policy is a sandbox's rules as they're kept: built-in lists by name, and
// rules as they're written. What Deny refuses stays refused whatever allows
// it.
type Policy struct {
	Presets []string `json:"presets,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

// Rules are the policy's rules, ready to be asked. What can't be read as a
// rule (a file changed by hand) is left out.
func (p Policy) Rules() *Rules {
	r := &Rules{}
	for _, n := range p.Presets {
		for _, h := range presets[n].hosts {
			if rule, err := ParseRule(h); err == nil {
				r.allow = append(r.allow, rule)
			}
		}
	}
	for _, s := range p.Allow {
		if rule, err := ParseRule(s); err == nil {
			r.allow = append(r.allow, rule)
		}
	}
	for _, s := range p.Deny {
		if rule, err := ParseRule(s); err == nil {
			r.deny = append(r.deny, rule)
		}
	}
	return r
}

func without(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func with(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// AllowWord puts a built-in list or a rule on the allow list, and takes the
// same rule off the deny list.
func (p *Policy) AllowWord(word string) error {
	if IsPreset(word) {
		p.Presets = with(p.Presets, word)
		return nil
	}
	rule, err := ParseRule(word)
	if err != nil {
		return Check(word)
	}
	p.Deny = without(p.Deny, rule.String())
	p.Allow = with(p.Allow, rule.String())
	return nil
}

// DenyWord puts a rule on the deny list and takes it off the allow list; a
// built-in list is taken off, and nothing of it is denied.
func (p *Policy) DenyWord(word string) error {
	if IsPreset(word) {
		p.Presets = without(p.Presets, word)
		return nil
	}
	rule, err := ParseRule(word)
	if err != nil {
		return err
	}
	p.Allow = without(p.Allow, rule.String())
	p.Deny = with(p.Deny, rule.String())
	return nil
}

// Blocked is a host a sandbox tried to reach and wasn't let.
type Blocked struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Why: it's on no list, a rule denies it, or where it leads isn't on
	// the internet.
	Why   string    `json:"why"`
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// maxBlocked is how many refused hosts a sandbox's list keeps: the latest.
const maxBlocked = 200

// Store keeps a sandbox's policy and the hosts it was refused, in a folder
// of its own. The launcher, the dashboard and the sandbox's gateway all
// write there, one at a time.
type Store struct{ Dir string }

func (s Store) policyFile() string  { return filepath.Join(s.Dir, "policy.json") }
func (s Store) blockedFile() string { return filepath.Join(s.Dir, "blocked.json") }

// locked runs fn while holding the folder's lock.
func (s Store) locked(fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func read(path string, into any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("can't read %s: %w", path, err)
	}
	return nil
}

func write(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Load reads the policy; one that was never written allows nothing.
func (s Store) Load() (Policy, error) {
	var p Policy
	err := read(s.policyFile(), &p)
	return p, err
}

// Stamp changes when the policy does.
func (s Store) Stamp() string {
	fi, err := os.Stat(s.policyFile())
	if err != nil {
		return "none"
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size())
}

// Update changes the policy: fn gets it as it is now, and what it leaves
// is kept, unless it fails.
func (s Store) Update(fn func(*Policy) error) error {
	return s.locked(func() error {
		var p Policy
		if err := read(s.policyFile(), &p); err != nil {
			return err
		}
		if err := fn(&p); err != nil {
			return err
		}
		return write(s.policyFile(), p)
	})
}

// Blocked lists the hosts the sandbox was refused, the latest first.
func (s Store) Blocked() ([]Blocked, error) {
	var list []Blocked
	if err := read(s.blockedFile(), &list); err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Last.After(list[j].Last) })
	return list, nil
}

// Refused notes that the sandbox was refused a host, now.
func (s Store) Refused(host string, port int, why string) error {
	return s.locked(func() error {
		var list []Blocked
		_ = read(s.blockedFile(), &list) // one that can't be read starts again
		now := time.Now().UTC()
		found := false
		for i := range list {
			// One line for what one rule would allow: a host's usual ports
			// together, any other port by itself.
			if Target(list[i].Host, list[i].Port) == Target(host, port) {
				list[i].Count++
				list[i].Last, list[i].Why, found = now, why, true
			}
		}
		if !found {
			list = append(list, Blocked{Host: host, Port: port, Why: why, Count: 1, First: now, Last: now})
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Last.After(list[j].Last) })
		if len(list) > maxBlocked {
			list = list[:maxBlocked]
		}
		return write(s.blockedFile(), list)
	})
}

// Forget takes what the rules now allow off the list of refused hosts.
func (s Store) Forget(r *Rules) error {
	return s.locked(func() error {
		var list, kept []Blocked
		if err := read(s.blockedFile(), &list); err != nil || len(list) == 0 {
			return err
		}
		for _, b := range list {
			if v, _ := r.Decide(b.Host, b.Port); v != Allowed {
				kept = append(kept, b)
			}
		}
		if len(kept) == len(list) {
			return nil
		}
		return write(s.blockedFile(), kept)
	})
}

// Remove deletes the folder and all it keeps.
func (s Store) Remove() error { return os.RemoveAll(s.Dir) }
