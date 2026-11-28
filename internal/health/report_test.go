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
		Version: "0.5.0", GOOS: goos, Machine: "amd64", Home: "/home/u", DockerCLI: true, Engine: "docker",
		Image: "opencode-sandbox:0.5.0", ImageOK: true,
		Config: "/home/u/.config/opencode/opencode.json", Providers: 2, PidsLimit: 1024, Gateway: true,
		Limits: "4 GB of memory, 4 CPUs",
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
				"only a disposable key", "seccomp; at most 1024 processes, 4 GB of memory, 4 CPUs (set others with `opencode-sandbox new --memory 8g",
				"used       Docker Desktop: one Linux VM", "install    Apple container",
				"`opencode-sandbox setup` installs it", "new sandboxes then use it",
				"Next step", "run        `opencode-sandbox setup` sets up Apple container: each new sandbox gets a lightweight VM of its own",
				"asks before doing it"},
			[]string{"gVisor", "AppArmor", "no seccomp", "OPENCODE_SANDBOX_ENGINE"}},
		{"Apple container used", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
			f.Agent = platform.Platform{NoAgentWhy: platform.AppleWhy}
		}, mac, true,
			[]string{"ok         Apple container 1.7.1", "image opencode-sandbox:0.5.0 in Apple container",
				"boundary   a lightweight VM of its own", "only a disposable key", "VM of its own, which can't reach",
				"each has a network of its own", "the VM has 4 GB of memory, 4 CPUs (set others with `opencode-sandbox new --memory 8g --cpus 6`)",
				"the VM stands in for them",
				"available  Docker Desktop: one Linux VM for all your containers; set OPENCODE_SANDBOX_ENGINE=docker to use it",
				"used       Apple container 1.7.1"},
			[]string{"  install    ", "seccomp", "Docker Desktop's Linux VM, shared", "Next step"}},
		{"Apple container used, no Docker", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
			f.Docker = nil
		}, mac, true, []string{"--         can't reach Docker; only sandboxes made with it need it", "used       Apple container"},
			[]string{"no   "}},
		{"Apple container running, Docker chosen", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.EngineEnv = true, "1.7.1", true, "docker"
		}, mac, true, []string{"available  Apple container", "1.7.1 is running; unset OPENCODE_SANDBOX_ENGINE", "used       Docker Desktop"},
			[]string{"  install    ", "Next step"}},
		{"Apple container stopped", func(f *Facts) {
			f.AppleCLI, f.AppleVersion = true, "1.7.1"
		}, mac, true, []string{"--         Apple container 1.7.1 is installed but not running", "start      Apple container",
			"1.7.1 is installed; `opencode-sandbox setup` (or `container system start`) starts it", "Next step"}, nil},
		{"Apple container chosen but stopped", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.Engine = true, "1.7.1", ""
			f.EngineErr = "OPENCODE_SANDBOX_ENGINE=apple, but Apple container isn't running here"
		}, mac, false, []string{"no         OPENCODE_SANDBOX_ENGINE=apple, but"}, []string{"How a new sandbox"}},
		{"Intel Mac", func(f *Facts) { f.AppleSilicon, f.Arch = false, "Intel" }, mac, true,
			[]string{"(Intel)", "no         Apple container needs a Mac with Apple silicon"}, []string{"  install    ", "Next step"}},
		{"old macOS", func(f *Facts) { f.MacOS, f.System = 15, "macOS 15.6" }, mac, true,
			[]string{"needs macOS 26 or later (this is macOS 15.6)"}, nil},
		{"Linux with Docker Engine", nil, linux, true,
			[]string{"Docker 29.0.0 (Ubuntu 24.04 LTS)", "this machine's kernel, shared",
				"stays on this machine", "no-new-privileges, AppArmor, seccomp", "Namespaces and seccomp separate",
				"used       Docker Engine", "install    gVisor", "`opencode-sandbox setup` installs it",
				"run        `opencode-sandbox setup` sets up gVisor: each new sandbox runs on a user-space kernel of its own"},
			[]string{"Apple", "trade-off", "rootless", "Kata"}},
		{"gVisor registered", func(f *Facts) { f.Docker.Runtimes["gvisor"] = backend.Runtime{Path: "/usr/local/bin/runsc"} }, linux, true,
			[]string{"set up     gVisor", "Docker has it as runtime gvisor, which can't reach the host's ssh-agent",
				"set OPENCODE_SANDBOX_RUNTIME=gvisor.", "used       Docker Engine", "`opencode-sandbox setup` adds a runtime that can",
				"sets up gVisor with your ssh-agent"}, []string{"  install    "}},
		{"gVisor registered twice", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Docker.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
			f.Runtime, f.RuntimeAuto = "runsc-ssh", true
		}, linux, true,
			[]string{"boundary   gVisor (runtime runsc-ssh, used because Docker has it)", "stays on this machine",
				"used       gVisor (runtime runsc-ssh, used because Docker has it)",
				"available  Docker Engine: sandboxes share this machine's kernel; set OPENCODE_SANDBOX_RUNTIME=runc to use it"},
			[]string{"host-uds", "Next step", "ssh-agent, for keys"}},
		{"gVisor registered twice, plain Docker asked for", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Docker.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
			f.Runtime, f.RuntimeEnv = "runc", "runc"
		}, linux, true,
			[]string{"used       Docker Engine", "available  gVisor", "runtime runsc-ssh; unset OPENCODE_SANDBOX_RUNTIME to use it"},
			[]string{"Next step"}},
		{"gVisor chosen", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Runtime, f.RuntimeEnv = "runsc", "runsc"
			f.Agent = platform.Platform{NoAgentWhy: "gVisor (runtime runsc) lets sandboxes connect to an ssh-agent on this machine only if Docker starts it with --host-uds=open"}
		}, linux, true,
			[]string{"boundary   gVisor (runtime runsc, chosen with OPENCODE_SANDBOX_RUNTIME)", "only a disposable key",
				"used       gVisor (runtime runsc, chosen", "`opencode-sandbox setup` adds a runtime that may reach the host's ssh-agent",
				"available  Docker Engine", "Next step"},
			[]string{"  install    ", "this machine's kernel, shared"}},
		{"gVisor chosen, though another reaches the agent", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc"}
			f.Docker.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
			f.Runtime, f.RuntimeEnv = "runsc", "runsc"
			f.Agent = platform.Platform{NoAgentWhy: "gVisor (runtime runsc) lets sandboxes connect to an ssh-agent on this machine only if Docker starts it with --host-uds=open"}
		}, linux, true,
			[]string{"used       gVisor (runtime runsc, chosen", "unset OPENCODE_SANDBOX_RUNTIME for a runtime that may reach"},
			[]string{"Next step"}},
		{"gVisor chosen, with host sockets", func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}}
			f.Runtime, f.RuntimeEnv = "runsc", "runsc"
		}, linux, true,
			[]string{"used       gVisor", "stays on this machine"}, []string{"host-uds", "Next step", "ssh-agent, for keys"}},
		{"limits that can't be read", func(f *Facts) {
			f.Limits, f.LimitsErr = "", `OPENCODE_SANDBOX_MEMORY: "lots" isn't an amount of memory; say it like 8g or 512m`
		}, linux, false, []string{`no         OPENCODE_SANDBOX_MEMORY: "lots" isn't an amount of memory`}, nil},
		{"a Docker that can't limit memory or CPUs", func(f *Facts) { f.Limits = "no memory or CPU limit" }, linux, true,
			[]string{"at most 1024 processes, no memory or CPU limit"}, nil},
		{"unknown runtime chosen", func(f *Facts) {
			f.RuntimeErr = "OPENCODE_SANDBOX_RUNTIME=nope, but Docker has no runtime by that name (it has: runc)"
		}, linux, false, []string{"no         OPENCODE_SANDBOX_RUNTIME=nope"}, nil},
		{"gVisor installed, not registered", func(f *Facts) { f.RunscPath = "/usr/bin/runsc" }, linux, true,
			[]string{"set up     gVisor", "Docker doesn't know it: `opencode-sandbox setup` registers it", "Next step"}, nil},
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
			[]string{"(Ubuntu 24.04 LTS, rootless)", "with your user's rights", "only a disposable key",
				"install    gVisor", "With rootless Docker, it isn't set up for you: https://gvisor.dev"}, []string{"Next step"}},
		{"Kata chosen", func(f *Facts) {
			f.Docker.Runtimes["kata"] = backend.Runtime{Path: "/opt/kata/bin/kata-runtime"}
			f.Runtime, f.RuntimeEnv = "kata", "kata"
		}, linux, true, []string{"used       Kata Containers", "boundary   Kata Containers"}, []string{"gVisor", "Next step"}},
		{"no gVisor build for this machine", func(f *Facts) { f.Machine, f.Arch = "riscv64", "riscv64" }, linux, true,
			[]string{"no         gVisor has no build for riscv64"}, []string{"Next step"}},
		{"Docker Desktop on Linux", func(f *Facts) {
			f.Docker.OS, f.InVM = "Docker Desktop", true
			f.Agent = platform.Platform{NoAgentWhy: "Docker Desktop runs containers in a VM"}
		}, linux, true,
			[]string{"Docker Desktop's Linux VM", "no         gVisor needs Docker Engine", "trade-off  Docker Engine instead"}, []string{"Next step"}},
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
			[]string{"Docker isn't installed: https://docs.docker.com/engine/install/", "install    gVisor",
				"Once Docker works, `opencode-sandbox setup` installs it"}, []string{"Next step"}},
		{"root", func(f *Facts) { f.Root = true }, linux, false, []string{"you're root"}, nil},
		{"no config", func(f *Facts) { f.ConfigErr = "missing" }, linux, false,
			[]string{"no opencode config at ~/.config/opencode/opencode.json"}, nil},
		{"config outside home", func(f *Facts) { f.Config = "/etc/oc.json"; f.ConfigErr = "not JSON" }, linux, false,
			[]string{"can't parse /etc/oc.json as JSON"}, nil},
		{"API keys stay here", nil, linux, true,
			[]string{"api keys   stay on this machine: a sandbox reaches its providers through a gateway", "can't read or copy their keys."},
			[]string{"Except", "can be read inside"}},
		{"API keys stay here, in Apple container too", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
		}, mac, true, []string{"api keys   stay on this machine"}, []string{"Except", "can be read inside"}},
		{"a provider the gateway can't serve", func(f *Facts) {
			f.KeysInside = []KeyInside{{"p9", "your opencode config doesn't say where its API is (options.baseURL)"}}
		}, linux, true,
			[]string{"api keys   stay on this machine", "Except a secret of p9, which goes into the sandbox: your opencode config doesn't say where its API is (options.baseURL)."},
			[]string{"can be read inside"}},
		{"two providers with secrets the gateway doesn't keep", func(f *Facts) {
			f.KeysInside = []KeyInside{{"p8", "no address"}, {"aws", "the gateway keeps a provider's options.apiKey and options.headers, not its options.secretAccessKey"}}
		}, linux, true,
			[]string{"Except a secret of p8, which goes into the sandbox: no address.",
				"Except a secret of aws, which goes into the sandbox: the gateway keeps a provider's options.apiKey and options.headers, not its options.secretAccessKey."}, nil},
		{"a network by default", nil, linux, true,
			[]string{"network    open: the internet, your local network and services on this machine", "`opencode-sandbox new --network filtered` makes one that reaches only its providers and the hosts you allow; `--network none`, only its providers."}, nil},
		{"a filtered network by default", func(f *Facts) { f.Filtered = true }, linux, true,
			[]string{"network    filtered (OPENCODE_SANDBOX_NETWORK): a sandbox reaches only its providers and the hosts on its list, through the gateway.",
				"`opencode-sandbox allow NAME HOST` puts a host on it", "a key from ~/.ssh or a disposable one stays on this machine"},
			[]string{"open: the internet", "nowhere to log in to"}},
		{"no network by default", func(f *Facts) { f.NoNetwork = true }, linux, true,
			[]string{"network    none (OPENCODE_SANDBOX_NETWORK): a sandbox reaches only its providers", "No SSH, no downloads", "`opencode-sandbox new --network open` makes one that can.",
				"ssh keys   none: without a network, a sandbox has nowhere to log in to"},
			[]string{"open: the internet", "a key from ~/.ssh"}},
		{"no network by default, in Apple container", func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine, f.NoNetwork = true, "1.7.1", true, "apple", true
		}, mac, true, []string{"network    none (OPENCODE_SANDBOX_NETWORK)"}, []string{"each has a network of its own"}},
		{"MCP servers behind the gateway", func(f *Facts) { f.MCPKept = 2 }, linux, true,
			[]string{"api keys   stay on this machine", "So do the headers and address of 2 MCP servers in your opencode config."},
			[]string{"Except"}},
		{"an MCP server that runs inside a sandbox", func(f *Facts) {
			f.MCPKept, f.MCPInside = 1, []KeyInside{{"files", "it runs inside the sandbox, with FILES_TOKEN in its environment"}}
		}, linux, true,
			[]string{"So do the headers and address of 1 MCP server in your opencode config.",
				"Except a secret of MCP server files, which goes into the sandbox: it runs inside the sandbox, with FILES_TOKEN in its environment."}, nil},
		{"the gateway switched off, with MCP servers", func(f *Facts) {
			f.Gateway, f.MCPKept, f.MCPInside = false, 2, []KeyInside{{"files", "x"}}
		}, linux, true,
			[]string{"which can be read inside the sandbox (OPENCODE_SANDBOX_GATEWAY is off); and the secrets of 3 MCP servers in your opencode config"},
			[]string{"stay on this machine", "Except"}},
		{"the gateway switched off", func(f *Facts) { f.Gateway, f.KeysInside = false, []KeyInside{{"p9", "x"}} }, linux, true,
			[]string{"api keys   only the chosen providers' keys, which can be read inside the sandbox (OPENCODE_SANDBOX_GATEWAY is off)"},
			[]string{"stay on this machine", "Except", "MCP"}},
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
				if strings.Contains(line, NBSP) || strings.HasSuffix(line, " ") {
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
	want := []string{"aa bb cc", "`x" + NBSP + "y" + NBSP + "z`", "https://example.com/a/very/long/url"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}
