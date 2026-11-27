package health

import (
	"bytes"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/platform"
)

func base(goos string) Facts {
	return Facts{
		Version: "0.5.0", GOOS: goos, Home: "/home/u", DockerCLI: true, Engine: "docker",
		Image: "opencode-sandbox:0.5.0", ImageOK: true,
		Config: "/home/u/.config/opencode/opencode.json", Providers: 2, PidsLimit: 1024,
	}
}

func mac() Facts {
	f := base("darwin")
	f.System, f.Arch, f.MacOS, f.AppleSilicon = "macOS 26.1", "Apple silicon", 26, true
	f.Docker = &backend.Info{OS: "Docker Desktop", Version: "29.9.3", DefaultRuntime: "runc",
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=cgroupns"},
		Runtimes:        map[string]backend.Runtime{"runc": {Path: "runc"}, "io.containerd.runc.v2": {Path: "runc"}}}
	f.InVM = true
	f.Agent = platform.Platform{NoAgentWhy: "on macOS, Docker runs containers in a VM that can't reach an ssh-agent on this machine"}
	return f
}

func linux() Facts {
	f := base("linux")
	f.System, f.Arch = "Ubuntu 24.04 LTS", "x86_64"
	f.Docker = &backend.Info{OS: "Ubuntu 24.04 LTS", Version: "29.0.0", DefaultRuntime: "runc",
		SecurityOptions: []string{"name=apparmor", "name=seccomp,profile=builtin", "name=cgroupns"},
		Runtimes:        map[string]backend.Runtime{"runc": {Path: "runc"}}}
	f.Agent = platform.Platform{AgentOK: true}
	return f
}

func TestReport(t *testing.T) {
	cases := []struct {
		name      string
		edit      func(*Facts)
		base      func() Facts
		ok        bool
		want, not []string
	}{
		{"mac with Docker Desktop", nil, mac, true,
			[]string{"on macOS 26.1 (Apple silicon)", "ok         Docker 29.9.3 (Docker Desktop)", "image opencode-sandbox:0.5.0 in Docker",
				"~/.config/opencode/opencode.json, 2 providers", "Docker Desktop's Linux VM, shared",
				"only a disposable key", "seccomp, at most 1024 processes",
				"used       Docker Desktop: one Linux VM", "install    Apple container",
				"`container system start`", "new sandboxes then use it"},
			[]string{"gVisor", "AppArmor", "no seccomp", "OPENCODE_SANDBOX_ENGINE"}},
		{"Apple container used", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
			f.Agent = platform.Platform{NoAgentWhy: platform.AppleWhy}
		}, mac, true,
			[]string{"ok         Apple container 1.7.1", "image opencode-sandbox:0.5.0 in Apple container",
				"boundary   a lightweight VM of its own", "only a disposable key", "VM of its own, which can't reach",
				"each has a network of its own", "the VM stands in for them",
				"available  Docker Desktop: one Linux VM for all your containers; set OPENCODE_SANDBOX_ENGINE=docker to use it",
				"used       Apple container 1.7.1"},
			[]string{"  install    ", "seccomp", "Docker Desktop's Linux VM, shared"}},
		{"Apple container used, no Docker", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
			f.Docker = nil
		}, mac, true, []string{"--         can't reach Docker; only sandboxes made with it need it", "used       Apple container"},
			[]string{"no   "}},
		{"Apple container running, Docker chosen", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning = true, "1.7.1", true
		}, mac, true, []string{"available  Apple container", "1.7.1 is running; unset OPENCODE_SANDBOX_ENGINE", "used       Docker Desktop"}, []string{"  install    "}},
		{"Apple container stopped", func(f *Facts) {
			f.AppleCLI, f.AppleVersion = true, "1.7.1"
		}, mac, true, []string{"--         Apple container 1.7.1 is installed but not running", "start      Apple container", "1.7.1 is installed"}, nil},
		{"Apple container chosen but stopped", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.Engine = true, "1.7.1", ""
			f.EngineErr = "OPENCODE_SANDBOX_ENGINE=apple, but Apple container isn't running here"
		}, mac, false, []string{"no         OPENCODE_SANDBOX_ENGINE=apple, but"}, []string{"How a new sandbox"}},
		{"Intel Mac", func(f *Facts) { f.AppleSilicon, f.Arch = false, "Intel" }, mac, true,
			[]string{"(Intel)", "no         Apple container needs a Mac with Apple silicon"}, []string{"  install    "}},
		{"old macOS", func(f *Facts) { f.MacOS, f.System = 15, "macOS 15.6" }, mac, true,
			[]string{"needs macOS 26 or later (this is macOS 15.6)"}, nil},
		{"Linux with Docker Engine", nil, linux, true,
			[]string{"Docker 29.0.0 (Ubuntu 24.04 LTS)", "this machine's kernel, shared",
				"stays on this machine", "no-new-privileges, AppArmor, seccomp", "Namespaces and seccomp separate",
				"used       Docker Engine", "install    gVisor", "`sudo runsc install`"},
			[]string{"Apple", "trade-off", "rootless", "Kata"}},
		{"gVisor registered", func(f *Facts) { f.Docker.Runtimes["gvisor"] = backend.Runtime{Path: "/usr/local/bin/runsc"} }, linux, true,
			[]string{"available  gVisor", "runtime gvisor; set OPENCODE_SANDBOX_RUNTIME=gvisor", "used       Docker Engine",
				"--runtime runsc-ssh -- --host-uds=open"}, []string{"  install    "}},
		{"gVisor registered twice", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Docker.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
		}, linux, true,
			[]string{"OPENCODE_SANDBOX_RUNTIME=runsc-ssh to use it."}, []string{"host-uds"}},
		{"gVisor chosen", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Runtime = "runsc"
			f.Agent = platform.Platform{NoAgentWhy: "gVisor (runtime runsc) lets sandboxes connect to an ssh-agent on this machine only if Docker starts it with --host-uds=open"}
		}, linux, true,
			[]string{"boundary   gVisor (runtime runsc, chosen with OPENCODE_SANDBOX_RUNTIME)", "only a disposable key",
				"used       gVisor (runtime runsc, chosen", "--runtime runsc-ssh -- --host-uds=open", "available  Docker Engine"},
			[]string{"  install    ", "this machine's kernel, shared"}},
		{"gVisor chosen, with host sockets", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
			f.Runtime = "runsc"
		}, linux, true,
			[]string{"used       gVisor", "stays on this machine"}, []string{"host-uds"}},
		{"unknown runtime chosen", func(f *Facts) {
			f.RuntimeErr = "OPENCODE_SANDBOX_RUNTIME=nope, but Docker has no runtime by that name (it has: runc)"
		}, linux, false, []string{"no         OPENCODE_SANDBOX_RUNTIME=nope"}, nil},
		{"gVisor installed, not registered", func(f *Facts) { f.RunscOnPath = true }, linux, true,
			[]string{"set up     gVisor", "Docker doesn't know it"}, nil},
		{"gVisor is Docker's default", func(f *Facts) {
			f.Docker.Runtimes["runsc"], f.Docker.DefaultRuntime = backend.Runtime{Path: "runsc"}, "runsc"
		}, linux, true,
			[]string{"boundary   gVisor (runtime runsc, Docker's default)", "used       gVisor (runtime runsc",
				"available  Docker Engine"}, []string{"  install    ", "this machine's kernel, shared"}},
		{"Kata registered", func(f *Facts) { f.Docker.Runtimes["kata"] = backend.Runtime{Path: "/opt/kata/bin/kata-runtime"} }, linux, true,
			[]string{"available  Kata Containers", "runtime kata; set OPENCODE_SANDBOX_RUNTIME=kata"}, nil},
		{"rootless Docker", func(f *Facts) {
			f.Docker.SecurityOptions = append(f.Docker.SecurityOptions, "name=rootless")
			f.Agent = platform.Platform{NoAgentWhy: "with rootless Docker or userns-remap, sandboxes don't run with your uid"}
		}, linux, true,
			[]string{"(Ubuntu 24.04 LTS, rootless)", "with your user's rights", "only a disposable key"}, nil},
		{"Docker Desktop on Linux", func(f *Facts) {
			f.Docker.OS, f.InVM = "Docker Desktop", true
			f.Agent = platform.Platform{NoAgentWhy: "Docker Desktop runs containers in a VM"}
		}, linux, true,
			[]string{"Docker Desktop's Linux VM", "no         gVisor needs Docker Engine", "trade-off  Docker Engine instead"}, nil},
		{"seccomp off", func(f *Facts) { f.Docker.SecurityOptions = []string{"name=seccomp,profile=unconfined"} }, linux, true,
			[]string{"no seccomp filter", "Namespaces separate"}, []string{"AppArmor", ", seccomp"}},
		{"image not built", func(f *Facts) { f.ImageOK = false }, linux, true,
			[]string{"--         image opencode-sandbox:0.5.0 isn't built in Docker yet"}, nil},
		{"OpenSSH missing", func(f *Facts) { f.MissingSSH = []string{"ssh-agent", "ssh-add"} }, linux, true,
			[]string{"--         missing ssh-agent, ssh-add"}, nil},
		{"Docker unreachable", func(f *Facts) { f.Docker, f.InVM, f.Engine = nil, false, "" }, mac, false,
			[]string{"no         can't reach Docker", "install    Apple container"},
			[]string{"How a new sandbox", "used ", "image"}},
		{"no Docker on Linux", func(f *Facts) { f.Docker, f.DockerCLI, f.Engine = nil, false, "" }, linux, false,
			[]string{"Docker isn't installed: https://docs.docker.com/engine/install/", "install    gVisor"}, nil},
		{"root", func(f *Facts) { f.Root = true }, linux, false, []string{"you're root"}, nil},
		{"no config", func(f *Facts) { f.ConfigErr = "missing" }, linux, false,
			[]string{"no opencode config at ~/.config/opencode/opencode.json"}, nil},
		{"config outside home", func(f *Facts) { f.Config = "/etc/oc.json"; f.ConfigErr = "not JSON" }, linux, false,
			[]string{"can't parse /etc/oc.json as JSON"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.base()
			if c.edit != nil {
				c.edit(&f)
			}
			var b bytes.Buffer
			ok := Report(&b, f)
			out := b.String()
			if ok != c.ok {
				t.Errorf("Report returned %v, want %v", ok, c.ok)
			}
			flat := strings.Join(strings.Fields(out), " ")
			for _, w := range c.want {
				if !strings.Contains(out, w) && !strings.Contains(flat, strings.Join(strings.Fields(w), " ")) {
					t.Errorf("missing %q", w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(out, n) {
					t.Errorf("unexpected %q", n)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if len(line) > 79 && !strings.Contains(line, "https://") {
					t.Errorf("line longer than 79 columns: %q", line)
				}
				if strings.Contains(line, nbsp) || strings.HasSuffix(line, " ") {
					t.Errorf("stray space in %q", line)
				}
			}
			if t.Failed() {
				t.Log("\n" + out)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	got := wrap("aa bb  cc "+cmd("x y z")+" https://example.com/a/very/long/url", 8)
	want := []string{"aa bb cc", "`x" + nbsp + "y" + nbsp + "z`", "https://example.com/a/very/long/url"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}
