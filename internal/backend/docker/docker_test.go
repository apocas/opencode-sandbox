package docker

import (
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

func TestCreateArgs(t *testing.T) {
	s := backend.Spec{
		Container: "opencode-sandbox-demo",
		TTY:       true,
		Labels:    [][2]string{{"opencode-sandbox.name", "demo"}, {"opencode-sandbox.keykind", "none"}},
		Init:      true, ReadOnlyRoot: true, DropAllCaps: true, NoNewPrivileges: true, PidsLimit: 1024,
		Tmpfs:   []string{"/tmp:exec,size=1g,mode=1777"},
		Network: "opencode-sandbox-net",
		Env:     [][2]string{{"TERM", "xterm"}, {"COLORTERM", ""}},
		Volumes: []string{"/home/agent", "/workspace"},
		Binds: []backend.Bind{
			{Src: "/run/u/configs/demo.json", Dst: "/home/agent/.config/opencode/opencode.json", ReadOnly: true},
			{Src: "/home/me/odd,dir/known_hosts", Dst: "/etc/ssh/ssh_known_hosts", ReadOnly: true},
		},
		Image: "opencode-sandbox:0.5.0",
		Cmd:   []string{"opencode", "--continue"},
	}
	want := []string{
		"docker", "create", "-it", "--name", "opencode-sandbox-demo",
		"--label", "opencode-sandbox.name=demo", "--label", "opencode-sandbox.keykind=none",
		"--init", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", "1024", "--tmpfs", "/tmp:exec,size=1g,mode=1777", "--network", "opencode-sandbox-net",
		"-e", "TERM=xterm", "-e", "COLORTERM=",
		"--mount", "type=volume,dst=/home/agent", "--mount", "type=volume,dst=/workspace",
		"--mount", "type=bind,src=/run/u/configs/demo.json,dst=/home/agent/.config/opencode/opencode.json,readonly",
		// a comma in a path is quoted, not taken as the next field
		"--mount", `type=bind,"src=/home/me/odd,dir/known_hosts",dst=/etc/ssh/ssh_known_hosts,readonly`,
		"opencode-sandbox:0.5.0", "opencode", "--continue",
	}
	if got := CreateArgs(s); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	s.TTY = false
	if got := CreateArgs(s); got[2] != "-i" {
		t.Errorf("without a terminal: got %q, want -i", got[2])
	}
	s.Runtime = "runsc"
	if got := strings.Join(CreateArgs(s), " "); !strings.Contains(got, "--label opencode-sandbox.keykind=none --runtime runsc --init") {
		t.Errorf("with a runtime: got %s", got)
	}
}

func TestParseInfo(t *testing.T) {
	info, err := parseInfo([]byte(`{"OS":"Docker Desktop","Version":"29.9.3","SecurityOptions":["name=seccomp,profile=builtin","name=cgroupns"],` +
		`"Runtimes":{"runc":{"path":"runc","status":{"x":"y"}},"gvisor":{"path":"/usr/bin/runsc","runtimeArgs":["--host-uds=open"]}},"DefaultRuntime":"runc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.OS != "Docker Desktop" || info.Version != "29.9.3" || info.DefaultRuntime != "runc" ||
		len(info.SecurityOptions) != 2 || info.Runtimes["gvisor"].Path != "/usr/bin/runsc" || info.Runtimes["gvisor"].Args[0] != "--host-uds=open" ||
		info.Runtimes["runc"].Path != "runc" {
		t.Errorf("got %+v", info)
	}
	if _, err := parseInfo([]byte("not json")); err == nil {
		t.Error("bad output was accepted")
	}
	if _, err := parseInfo([]byte(`{"OS":"","Version":"","SecurityOptions":null,"Runtimes":null,"DefaultRuntime":""}`)); err == nil {
		t.Error("docker info without a daemon was accepted")
	}
}

// A sandbox without a network is on none, whatever network it names.
func TestCreateArgsNoNetwork(t *testing.T) {
	got := strings.Join(CreateArgs(backend.Spec{Container: "c", Network: "opencode-sandbox-net", NoNetwork: true, Image: "i"}), " ")
	if got != "docker create -i --name c --network none i" {
		t.Errorf("got %q", got)
	}
}
