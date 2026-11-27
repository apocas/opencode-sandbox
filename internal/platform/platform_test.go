package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

func info() backend.Info {
	return backend.Info{OS: "Ubuntu 24.04 LTS", DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{
		"runc":          {Path: "runc"},
		"runsc":         {Path: "/usr/local/bin/runsc"},
		"runsc-ssh":     {Path: "/usr/local/bin/runsc", Args: []string{"--platform=systrap", "--host-uds=open"}},
		"sandboxed":     {Path: "/opt/gvisor/runsc"},
		"sandboxed-ssh": {Path: "/opt/gvisor/runsc", Args: []string{"--host-uds=all"}},
		"kata":          {Path: "/opt/kata/bin/containerd-shim-kata-v2"},
	}}
}

func TestKind(t *testing.T) {
	for name, want := range map[string]string{"runc": "", "runsc": Gvisor, "runsc-ssh": Gvisor, "sandboxed": Gvisor, "sandboxed-ssh": Gvisor,
		"kata": Kata, "": "", "gvisor-unknown": Gvisor, "crun": ""} {
		if got := Kind(info(), name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

func TestHostUDS(t *testing.T) {
	for args, want := range map[string]bool{"--host-uds=open": true, "--host-uds=all": true, "-host-uds=open": true,
		"--host-uds open": true, "--host-uds=create": false, "--host-uds=none": false, "": false, "--host-udsx=open": false} {
		if got := HostUDS(backend.Runtime{Args: strings.Fields(args)}); got != want {
			t.Errorf("%q: got %v, want %v", args, got, want)
		}
	}
}

func TestDecideOn(t *testing.T) {
	t.Setenv("OPENCODE_SANDBOX_NO_AGENT", "")
	for chosen, want := range map[string]string{"": "", "runc": "", "runsc-ssh": "", "runsc": "gVisor (runtime runsc)", "kata": "Kata"} {
		p := DecideOn("linux", info(), chosen)
		if (want == "") != p.AgentOK || !strings.HasPrefix(p.NoAgentWhy, want) || p.Runtime != chosen {
			t.Errorf("%q: got %+v, want reason %q", chosen, p, want)
		}
	}
	gv := info()
	gv.DefaultRuntime = "runsc"
	if p := DecideOn("linux", gv, ""); p.AgentOK {
		t.Error("gVisor as Docker's default runtime was allowed an agent")
	}
	if p := DecideOn("darwin", info(), ""); p.AgentOK || !strings.Contains(p.NoAgentWhy, "on macOS") {
		t.Errorf("macOS: got %+v", p)
	}
	rootless := info()
	rootless.SecurityOptions = []string{"name=seccomp,profile=builtin", "name=rootless"}
	if p := DecideOn("linux", rootless, "runsc-ssh"); p.AgentOK || !strings.Contains(p.NoAgentWhy, "rootless") {
		t.Errorf("rootless: got %+v", p)
	}
	t.Setenv("OPENCODE_SANDBOX_NO_AGENT", "1")
	if p := DecideOn("linux", info(), ""); p.AgentOK {
		t.Error("OPENCODE_SANDBOX_NO_AGENT was ignored")
	}
}

func TestCheckRuntimes(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	here := filepath.Join(t.TempDir(), "runsc")
	if err := os.WriteFile(here, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	i := backend.Info{OS: "Ubuntu 24.04 LTS", DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{
		"runc": {Path: "runc"}, "runsc": {Path: here}, "runsc-ssh": {Path: here + "-gone"}}}
	got := CheckRuntimes(i)
	if runtime.GOOS != "linux" { // Docker runs in a VM here: nothing to look at
		if got.Runtimes["runsc-ssh"].Missing {
			t.Error("a runtime in Docker's VM was looked for on this machine")
		}
		return
	}
	if got.Runtimes["runc"].Missing || got.Runtimes["runsc"].Missing || !got.Runtimes["runsc-ssh"].Missing {
		t.Errorf("got %+v", got.Runtimes)
	}
	if i.Runtimes["runsc-ssh"].Missing {
		t.Error("the info passed in was changed")
	}
	if err := CheckRuntime(got, "runsc-ssh"); err == nil || !strings.Contains(err.Error(), "isn't on this machine any more") {
		t.Errorf("asking for it: got %v", err)
	}
	if rt := RuntimeOf(got, Gvisor); rt != "runsc" {
		t.Errorf("RuntimeOf: got %q", rt)
	}
	t.Setenv("DOCKER_HOST", "ssh://elsewhere")
	if CheckRuntimes(i).Runtimes["runsc-ssh"].Missing {
		t.Error("a runtime on another machine was looked for on this one")
	}
}

func TestCheckRuntime(t *testing.T) {
	if CheckRuntime(info(), "") != nil || CheckRuntime(info(), "runsc") != nil {
		t.Error("a known runtime was refused")
	}
	err := CheckRuntime(info(), "nope")
	if err == nil || !strings.Contains(err.Error(), "it has: kata, runc, runsc, runsc-ssh, sandboxed, sandboxed-ssh") {
		t.Errorf("got %v", err)
	}
}

func TestChooseRuntime(t *testing.T) {
	t.Setenv("OPENCODE_SANDBOX_NO_AGENT", "")
	only := func(names ...string) backend.Info {
		all, i := info(), info()
		i.Runtimes = map[string]backend.Runtime{"runc": all.Runtimes["runc"]}
		for _, n := range names {
			i.Runtimes[n] = all.Runtimes[n]
		}
		return i
	}
	rootless := only("runsc")
	rootless.SecurityOptions = []string{"name=rootless"}
	byDefault := only("runsc")
	byDefault.DefaultRuntime = "runsc"
	gone := only("runsc-ssh")
	gone.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}, Missing: true}
	defaultAnd := func(names ...string) backend.Info {
		i := only(append(names, "runsc")...)
		i.DefaultRuntime = "runsc"
		return i
	}
	for name, c := range map[string]struct {
		goos, want string
		info       backend.Info
		rt         string
		auto       bool
	}{
		"nothing but runc":                      {"linux", "", only(), "", false},
		"gVisor that can reach the agent":       {"linux", "", only("runsc", "runsc-ssh"), "runsc-ssh", true},
		"gVisor that would cost the agent":      {"linux", "", only("runsc"), "", false},
		"gVisor where there's no agent anyway":  {"linux", "", rootless, "runsc", true},
		"gVisor in a VM, as on macOS":           {"darwin", "", only("runsc"), "runsc", true},
		"asked for":                             {"linux", "runsc", only("runsc", "runsc-ssh"), "runsc", false},
		"asked for plain Docker":                {"linux", "runc", only("runsc", "runsc-ssh"), "runc", false},
		"gVisor is Docker's default":            {"linux", "", byDefault, "", false},
		"Docker's default gVisor has no agent":  {"linux", "", defaultAnd("runsc-ssh"), "runsc-ssh", true},
		"no agent anyway: Docker's default":     {"darwin", "", defaultAnd("runsc-ssh"), "", false},
		"no agent anyway: the stricter one":     {"darwin", "", only("runsc-ssh", "runsc"), "runsc", true},
		"two that reach the agent: by name":     {"linux", "", only("runsc-ssh", "sandboxed-ssh"), "runsc-ssh", true},
		"Kata is never picked without asking":   {"linux", "", only("kata"), "", false},
		"a runtime Docker lacks stays the wish": {"linux", "nope", only(), "nope", false},
		"gVisor whose program is gone":          {"linux", "", gone, "", false},
	} {
		if rt, auto := ChooseRuntime(c.goos, c.info, c.want); rt != c.rt || auto != c.auto {
			t.Errorf("%s: got %q (auto %v), want %q (auto %v)", name, rt, auto, c.rt, c.auto)
		}
	}
	t.Setenv("OPENCODE_SANDBOX_NO_AGENT", "1")
	if rt, auto := ChooseRuntime("linux", only("runsc"), ""); rt != "runsc" || !auto {
		t.Errorf("with OPENCODE_SANDBOX_NO_AGENT: got %q (auto %v)", rt, auto)
	}
}

func TestRuntimeOf(t *testing.T) {
	if got := RuntimeOf(info(), Gvisor); got != "runsc-ssh" {
		t.Errorf("gVisor: got %q, want the one that reaches host sockets", got)
	}
	if got := RuntimeOf(info(), Kata); got != "kata" {
		t.Errorf("Kata: got %q", got)
	}
	if got := RuntimeOf(backend.Info{DefaultRuntime: "runc"}, Gvisor); got != "" {
		t.Errorf("none: got %q", got)
	}
}
