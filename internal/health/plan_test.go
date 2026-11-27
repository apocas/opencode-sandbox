package health

import (
	"bytes"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

// cmds flattens a plan to one line for each command, "fetch NAME" for a
// download.
func cmds(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		if s.Fetch != "" {
			out = append(out, "fetch "+s.Fetch)
		}
		for _, c := range s.Cmds {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

func TestPlan(t *testing.T) {
	gv := backend.Runtime{Path: "/opt/gvisor/runsc"}
	gvSSH := backend.Runtime{Path: "/opt/gvisor/runsc", Args: []string{"--host-uds=open"}}
	install := []string{
		"fetch gvisor",
		"sudo install -m 755 {downloads}/runsc {downloads}/containerd-shim-runsc-v1 /usr/local/bin/",
		"sudo cp -R {downloads}/gvisor-bin /usr/local/bin/",
	}
	cases := []struct {
		name  string
		base  func() Facts
		edit  func(*Facts)
		what  string
		cmds  []string
		best  bool
		note  string // part of Note
		after string // part of After
	}{
		{"mac without Apple container", mac, nil, "Apple container", []string{
			"fetch apple",
			"sudo /usr/sbin/installer -pkg {downloads}/container-installer.pkg -target /",
			"/usr/local/bin/container system start --enable-kernel-install",
		}, false, "", ""},
		{"mac without anything", mac, func(f *Facts) { f.Docker, f.DockerCLI, f.Engine = nil, false, "" }, "Apple container",
			[]string{"fetch apple", "sudo /usr/sbin/installer -pkg {downloads}/container-installer.pkg -target /",
				"/usr/local/bin/container system start --enable-kernel-install"}, false, "", ""},
		{"mac, Docker asked for", mac, func(f *Facts) { f.EngineEnv = "docker" }, "Apple container",
			[]string{"fetch apple", "sudo /usr/sbin/installer -pkg {downloads}/container-installer.pkg -target /",
				"/usr/local/bin/container system start --enable-kernel-install"}, false, "", "OPENCODE_SANDBOX_ENGINE=docker is set"},
		{"Apple container stopped", mac, func(f *Facts) { f.AppleCLI, f.AppleVersion = true, "1.7.1" }, "Apple container",
			[]string{"container system start --enable-kernel-install"}, false, "", ""},
		{"Apple container used", mac, func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.Engine = true, "1.7.1", true, "apple"
		}, "", nil, true, "already gets a VM of its own (Apple container 1.7.1)", ""},
		{"Apple container running, Docker asked for", mac, func(f *Facts) {
			f.AppleCLI, f.AppleVersion, f.AppleRunning, f.EngineEnv = true, "1.7.1", true, "docker"
		}, "", nil, false, "OPENCODE_SANDBOX_ENGINE=docker keeps new sandboxes out of it", ""},
		{"Intel Mac", mac, func(f *Facts) { f.AppleSilicon = false }, "", nil, false,
			"needs a Mac with Apple silicon. Sandboxes here share Docker Desktop's Linux VM", ""},
		{"Intel Mac without Docker", mac, func(f *Facts) { f.AppleSilicon, f.Docker, f.Engine = false, nil, "" }, "", nil, false,
			"sandboxes need Docker: https://docs.docker.com/get-started/get-docker/", ""},
		{"old macOS", mac, func(f *Facts) { f.MacOS, f.System = 15, "macOS 15.6" }, "", nil, false,
			"needs macOS 26 or later (this is macOS 15.6)", ""},

		{"Linux with Docker Engine", linux, nil, "gVisor", append(install[:3:3],
			"sudo /usr/local/bin/runsc install",
			"sudo /usr/local/bin/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"), false, "", ""},
		{"Docker under systemd", linux, func(f *Facts) { f.Systemd = true }, "gVisor", append(install[:3:3],
			"sudo /usr/local/bin/runsc install",
			"sudo /usr/local/bin/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo systemctl reload docker"), false, "", ""},
		{"userns-remap: no ssh-agent to reach", linux, func(f *Facts) {
			f.Docker.SecurityOptions = append(f.Docker.SecurityOptions, "name=userns")
		}, "gVisor", append(install[:3:3], "sudo /usr/local/bin/runsc install", "sudo pkill -HUP -x dockerd"), false, "", ""},
		{"runsc installed, not registered", linux, func(f *Facts) { f.RunscPath = "/usr/bin/runsc" }, "gVisor", []string{
			"sudo /usr/bin/runsc install",
			"sudo /usr/bin/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"}, false, "", ""},
		{"gVisor registered without the agent", linux, func(f *Facts) { f.Docker.Runtimes["sandboxed"] = gv }, "gVisor with your ssh-agent", []string{
			"sudo /opt/gvisor/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"}, false, "", ""},
		{"gVisor is Docker's default, found on PATH", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"], f.Docker.DefaultRuntime = backend.Runtime{Path: "runsc"}, "runsc"
		}, "gVisor with your ssh-agent", []string{
			"sudo runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"}, false, "", ""},
		{"gVisor chosen without the agent", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"] = gv
			f.Runtime, f.RuntimeEnv = "runsc", "runsc"
		}, "gVisor with your ssh-agent", []string{
			"sudo /opt/gvisor/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"}, false, "", "OPENCODE_SANDBOX_RUNTIME=runsc is set, a runtime that can't reach your ssh-agent"},
		{"gVisor in use", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"], f.Docker.Runtimes["runsc-ssh"] = gv, gvSSH
			f.Runtime, f.RuntimeAuto = "runsc-ssh", true
		}, "", nil, true, "already runs on a kernel of its own (gVisor, runtime runsc-ssh, used because Docker has it)", ""},
		{"gVisor in use, userns-remap", linux, func(f *Facts) {
			f.Docker.SecurityOptions = append(f.Docker.SecurityOptions, "name=userns")
			f.Docker.Runtimes["runsc"] = gv
			f.Runtime, f.RuntimeAuto = "runsc", true
		}, "", nil, true, "already runs on a kernel of its own", ""},
		{"gVisor there, plain Docker asked for", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"], f.Docker.Runtimes["runsc-ssh"] = gv, gvSSH
			f.Runtime, f.RuntimeEnv = "runc", "runc"
		}, "", nil, false, "gVisor is set up, but OPENCODE_SANDBOX_RUNTIME=runc is set, which keeps new sandboxes off gVisor: unset it.", ""},
		{"gVisor there, the one without the agent asked for", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"], f.Docker.Runtimes["runsc-ssh"] = gv, gvSSH
			f.Runtime, f.RuntimeEnv = "runsc", "runsc"
		}, "", nil, false, "gVisor is set up, but OPENCODE_SANDBOX_RUNTIME=runsc is set, a runtime that can't reach your ssh-agent", ""},
		{"a runtime Docker lacks asked for", linux, func(f *Facts) {
			f.RuntimeEnv, f.RuntimeErr = "nope", "OPENCODE_SANDBOX_RUNTIME=nope, but Docker has no runtime by that name"
		}, "gVisor", append(install[:3:3],
			"sudo /usr/local/bin/runsc install",
			"sudo /usr/local/bin/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"), false, "", "OPENCODE_SANDBOX_RUNTIME=nope is set, which keeps new sandboxes off gVisor"},
		{"gVisor registered, its program gone", linux, func(f *Facts) {
			f.Docker.Runtimes["runsc"] = backend.Runtime{Path: "/usr/local/bin/runsc", Missing: true}
			f.Docker.Runtimes["runsc-ssh"] = backend.Runtime{Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}, Missing: true}
		}, "gVisor", append(install[:3:3],
			"sudo /usr/local/bin/runsc install",
			"sudo /usr/local/bin/runsc install --runtime runsc-ssh -- --host-uds=open",
			"sudo pkill -HUP -x dockerd"), false, "", ""},
		{"Kata in use", linux, func(f *Facts) {
			f.Docker.Runtimes["kata"] = backend.Runtime{Path: "/opt/kata/bin/kata-runtime"}
			f.Runtime, f.RuntimeEnv = "kata", "kata"
		}, "", nil, true, "already runs in a VM of its own (Kata Containers", ""},
		{"no Docker", linux, func(f *Facts) { f.Docker, f.DockerCLI, f.Engine = nil, false, "" }, "", nil, false,
			"Docker isn't installed, and sandboxes need it here: https://docs.docker.com/engine/install/", ""},
		{"Docker out of reach", linux, func(f *Facts) { f.Docker, f.Engine = nil, "" }, "", nil, false, "can't reach Docker", ""},
		{"Docker Desktop on Linux", linux, func(f *Facts) { f.Docker.OS, f.InVM = "Docker Desktop", true }, "", nil, false,
			"Docker Desktop keeps all your containers in one Linux VM", ""},
		{"rootless Docker", linux, func(f *Facts) {
			f.Docker.SecurityOptions = append(f.Docker.SecurityOptions, "name=rootless")
		}, "", nil, false, "isn't set up automatically with rootless Docker", ""},
		{"no gVisor build", linux, func(f *Facts) { f.Machine, f.Arch = "riscv64", "riscv64" }, "", nil, false, "has no build for riscv64", ""},
		{"another system", func() Facts { f := base("freebsd"); f.System, f.Engine, f.DockerCLI = "FreeBSD", "", false; return f }, nil, "", nil, false, "nothing opencode-sandbox can set up on FreeBSD", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.base()
			if c.edit != nil {
				c.edit(&f)
			}
			p := PlanFor(f)
			if got, want := strings.Join(cmds(p), "\n"), strings.Join(c.cmds, "\n"); got != want {
				t.Errorf("commands:\n%s\nwant:\n%s", got, want)
			}
			if p.What != c.what || p.Best != c.best {
				t.Errorf("got what %q, best %v; want %q, %v", p.What, p.Best, c.what, c.best)
			}
			if !strings.Contains(p.Note, c.note) || (c.note == "") != (p.Note == "") {
				t.Errorf("note: got %q, want %q", p.Note, c.note)
			}
			if !strings.Contains(p.After, c.after) || (c.after == "") != (p.After == "") {
				t.Errorf("after: got %q, want %q", p.After, c.after)
			}
			// A plan has steps, or says why not; one with steps says what for.
			if (len(p.Steps) > 0) == (p.Note != "") || (len(p.Steps) > 0) != (p.Gain != "") || p.Best && len(p.Steps) > 0 {
				t.Errorf("inconsistent plan: %+v", p)
			}
			for _, s := range p.Steps {
				if s.Title == "" || (s.Fetch == "") == (len(s.Cmds) == 0) {
					t.Errorf("odd step: %+v", s)
				}
			}
			if sudo := strings.Contains(strings.Join(cmds(p), "\n"), "sudo "); p.Sudo() != sudo {
				t.Errorf("Sudo() = %v", p.Sudo())
			}
			// The report offers setup exactly when setup has something to do.
			var b bytes.Buffer
			Report(&b, f)
			flat := strings.Join(strings.Fields(b.String()), " ")
			if offers := strings.Contains(flat, "Next step run `opencode-sandbox setup` sets up "+p.What+":"); offers != (len(p.Steps) > 0) {
				t.Errorf("the report offers setup: %v, with %d steps\n%s", offers, len(p.Steps), b.String())
			}
			if len(p.Steps) == 0 && strings.Contains(flat, "`opencode-sandbox setup` ") && !strings.Contains(flat, "Once Docker works") {
				t.Errorf("the report mentions setup, which would do nothing:\n%s", b.String())
			}
		})
	}
}
