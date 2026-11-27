package apple

import (
	"strings"
	"testing"
	"time"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

func TestCreateArgs(t *testing.T) {
	s := backend.Spec{
		Container: "opencode-sandbox-demo",
		TTY:       true,
		Labels:    [][2]string{{"opencode-sandbox.name", "demo"}},
		Init:      true, ReadOnlyRoot: true, DropAllCaps: true, NoNewPrivileges: true, PidsLimit: 1024,
		Tmpfs:   []string{"/tmp:exec,size=1g,mode=1777"},
		Network: "opencode-sandbox-net",
		Env:     [][2]string{{"TERM", "xterm"}},
		Volumes: []string{"/home/agent", "/workspace"},
		Binds:   []backend.Bind{{Src: "/run/u/sandboxes/demo", Dst: "/run/opencode-sandbox/config", ReadOnly: true}},
		Image:   "opencode-sandbox:0.5.0",
		Cmd:     []string{"opencode"},
	}
	got, err := CreateArgs(s, "opencode-sandbox-demo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"container", "create", "-i", "-t", "--name", "opencode-sandbox-demo",
		"--label", "opencode-sandbox.name=demo",
		"--init", "--read-only", "--cap-drop", "ALL", "--memory", "4G", "--cpus", "4",
		"--tmpfs", "/tmp:exec,size=1g,mode=1777", "--network", "opencode-sandbox-demo",
		"-e", "TERM=xterm",
		"-v", "opencode-sandbox-demo-home-agent:/home/agent", "-v", "opencode-sandbox-demo-workspace:/workspace",
		"--mount", "type=bind,source=/run/u/sandboxes/demo,target=/run/opencode-sandbox/config,readonly",
		"opencode-sandbox:0.5.0", "opencode",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	s.Binds[0].Src = "/odd,dir"
	if _, err := CreateArgs(s, "n"); err == nil {
		t.Error("a path with a comma was accepted")
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "Less than a second", time.Second: "1 second", 30 * time.Second: "30 seconds",
		time.Minute: "About a minute", 5 * time.Minute: "5 minutes", time.Hour: "About an hour",
		3 * time.Hour: "3 hours", 72 * time.Hour: "3 days", 21 * 24 * time.Hour: "3 weeks",
		90 * 24 * time.Hour: "3 months", 3 * 365 * 24 * time.Hour: "3 years",
	} {
		if got := HumanDuration(d); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
}

func TestStatus(t *testing.T) {
	now := time.Now()
	started := now.Add(-5 * time.Minute)
	var c container
	c.Status.State = "stopped"
	if got := status(c, now); got != "Exited" {
		t.Errorf("no start on record (never started, or the service restarted since): got %q", got)
	}
	c.Status.StartedDate = &started
	c.Status.State = "stopped"
	if got := status(c, now); got != "Exited" {
		t.Errorf("stopped: got %q", got)
	}
	c.Status.State = "running"
	if got := status(c, now); got != "Up 5 minutes" {
		t.Errorf("running: got %q", got)
	}
}
