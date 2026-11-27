package platform

import (
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

func info() backend.Info {
	return backend.Info{OS: "Ubuntu 24.04 LTS", DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{
		"runc":      {Path: "runc"},
		"runsc":     {Path: "/usr/local/bin/runsc"},
		"runsc-ssh": {Path: "/usr/local/bin/runsc", Args: []string{"--platform=systrap", "--host-uds=open"}},
		"sandboxed": {Path: "/opt/gvisor/runsc"},
		"kata":      {Path: "/opt/kata/bin/containerd-shim-kata-v2"},
	}}
}

func TestKind(t *testing.T) {
	for name, want := range map[string]string{"runc": "", "runsc": Gvisor, "runsc-ssh": Gvisor, "sandboxed": Gvisor,
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

func TestCheckRuntime(t *testing.T) {
	if CheckRuntime(info(), "") != nil || CheckRuntime(info(), "runsc") != nil {
		t.Error("a known runtime was refused")
	}
	err := CheckRuntime(info(), "nope")
	if err == nil || !strings.Contains(err.Error(), "it has: kata, runc, runsc, runsc-ssh, sandboxed") {
		t.Errorf("got %v", err)
	}
}
