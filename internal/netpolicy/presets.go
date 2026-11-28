package netpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// presets are built-in lists of hosts, by name: what a common tool needs to
// do its job. They leave out hosts that serve anyone's content (pages,
// buckets), which would be a way out for whatever runs in a sandbox.
var presets = map[string]struct {
	about string
	hosts []string
}{
	"github": {"git, the API and release downloads of github.com", []string{
		"github.com", "api.github.com", "codeload.github.com", "uploads.github.com",
		"ssh.github.com:443", "objects.githubusercontent.com", "raw.githubusercontent.com",
		"release-assets.githubusercontent.com", "github-releases.githubusercontent.com",
		"media.githubusercontent.com", "github-cloud.githubusercontent.com",
	}},
	"gitlab": {"git and the API of gitlab.com", []string{"gitlab.com", "altssh.gitlab.com:443"}},
	"npm":    {"packages for npm, pnpm and yarn", []string{"registry.npmjs.org", "registry.yarnpkg.com"}},
	"pypi":   {"packages for pip and uv", []string{"pypi.org", "files.pythonhosted.org"}},
	"go":     {"Go modules and toolchains", []string{"proxy.golang.org", "sum.golang.org", "go.dev", "golang.org", "dl.google.com"}},
}

// Preset is a built-in list of hosts.
type Preset struct {
	Name, About string
	Hosts       []string
}

// Presets lists the built-in lists, by name.
func Presets() []Preset {
	var out []Preset
	for n, p := range presets {
		out = append(out, Preset{Name: n, About: p.about, Hosts: append([]string(nil), p.hosts...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IsPreset says whether there's a built-in list by that name.
func IsPreset(n string) bool {
	_, ok := presets[n]
	return ok
}

// presetNames is "github, gitlab, go, npm and pypi".
func presetNames() string {
	var names []string
	for _, p := range Presets() {
		names = append(names, p.Name)
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Check says why a word can't go on an allow list: it's neither a built-in
// list nor a rule. nil if it can.
func Check(word string) error {
	if IsPreset(word) {
		return nil
	}
	_, err := ParseRule(word)
	if err != nil && !strings.ContainsAny(word, ".:/*") {
		return fmt.Errorf("%q is neither a host nor one of the built-in lists (%s)", word, presetNames())
	}
	return err
}
