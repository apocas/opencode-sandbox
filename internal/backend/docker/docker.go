// Package docker implements backend.Engine with the docker CLI.
package docker

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/run"
)

// Docker drives the docker CLI found on PATH.
type Docker struct{ R run.Runner }

var _ backend.Engine = (*Docker)(nil)

func (d *Docker) Name() string { return "docker" }

func (d *Docker) Caps() backend.Caps {
	return backend.Caps{FileBinds: true, Attach: true, NoNewPrivileges: true, PidsLimit: true, CopyNeedsBinds: true}
}

func (d *Docker) output(args ...string) (string, error) {
	return run.Output(d.R, append([]string{"docker"}, args...)...)
}

// check runs docker with stdout discarded and stderr shown.
func (d *Docker) check(args ...string) error {
	return run.Check(d.R, &run.Cmd{Args: append([]string{"docker"}, args...), Stderr: run.Stderr})
}

func (d *Docker) quiet(args ...string) int {
	return run.Quiet(d.R, append([]string{"docker"}, args...)...)
}

func (d *Docker) Exists(c string) bool {
	return d.quiet("container", "inspect", c) == 0
}

func (d *Docker) State(c string) string {
	var out bytes.Buffer
	code, err := d.R.Run(&run.Cmd{Args: []string{"docker", "container", "inspect", "-f", "{{.State.Status}}", c}, Stdout: &out})
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimRight(out.String(), "\n")
}

func (d *Docker) Label(c, label string) (string, error) {
	return d.output("container", "inspect", "-f", fmt.Sprintf("{{index .Config.Labels %q}}", label), c)
}

func (d *Docker) Names(label string) ([]string, error) {
	out, err := d.output("ps", "-a", "--filter", "label="+label, "--format", fmt.Sprintf("{{.Label %q}}", label))
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (d *Docker) Rows(label string) ([]backend.Row, error) {
	l := func(k string) string { return fmt.Sprintf("{{.Label %q}}", k) }
	format := strings.Join([]string{
		l(label), l("opencode-sandbox.keykind"), l("opencode-sandbox.key"), "{{.Status}}", "{{.RunningFor}}",
		l("opencode-sandbox.source"), l("opencode-sandbox.providers"), l("opencode-sandbox.keymode"),
		l("opencode-sandbox.runtime"), l("opencode-sandbox.version"), l("opencode-sandbox.gateway"),
		l("opencode-sandbox.network"),
	}, "\t")
	out, err := d.output("ps", "-a", "--filter", "label="+label, "--format", format)
	if err != nil {
		return nil, err
	}
	var rows []backend.Row
	for _, line := range lines(out) {
		f := append(strings.Split(line, "\t"), make([]string, 12)...)
		rows = append(rows, backend.Row{
			Name: f[0], KeyKind: f[1], Key: f[2], Status: f[3], Created: f[4],
			Source: f[5], Providers: f[6], KeyMode: f[7], Runtime: f[8], Engine: "docker", Version: f[9], Gateway: f[10], Network: f[11],
		})
	}
	return rows, nil
}

func (d *Docker) BindSources(c string) ([]string, error) {
	out, err := d.output("container", "inspect", "-f", "{{json .Mounts}}", c)
	if err != nil {
		return nil, err
	}
	var mounts []struct{ Type, Source string }
	if err := json.Unmarshal([]byte(out), &mounts); err != nil {
		return nil, fmt.Errorf("can't read the mounts of %s: %w", c, err)
	}
	var srcs []string
	for _, m := range mounts {
		if m.Type == "bind" {
			srcs = append(srcs, m.Source)
		}
	}
	return srcs, nil
}

func (d *Docker) ImageExists(ref string) bool {
	return d.quiet("image", "inspect", ref) == 0
}

func (d *Docker) Build(ref, contextDir string, buildArgs [][2]string, extra []string, out io.Writer) error {
	args := []string{"docker", "build", "-t", ref}
	for _, a := range buildArgs {
		args = append(args, "--build-arg", a[0]+"="+a[1])
	}
	args = append(append(args, extra...), contextDir)
	return run.Check(d.R, &run.Cmd{Args: args, Stdout: out, Stderr: out})
}

func (d *Docker) EnsureNetwork(name string) error {
	if d.quiet("network", "inspect", name) == 0 {
		return nil
	}
	return d.check("network", "create", "-o", "com.docker.network.bridge.enable_icc=false", name)
}

// CreateArgs is the docker command for s.
func CreateArgs(s backend.Spec) []string {
	tty := "-i"
	if s.TTY {
		tty = "-it"
	}
	a := []string{"docker", "create", tty, "--name", s.Container}
	for _, l := range s.Labels {
		a = append(a, "--label", l[0]+"="+l[1])
	}
	if s.Runtime != "" {
		a = append(a, "--runtime", s.Runtime)
	}
	if s.Init {
		a = append(a, "--init")
	}
	if s.ReadOnlyRoot {
		a = append(a, "--read-only")
	}
	if s.DropAllCaps {
		a = append(a, "--cap-drop", "ALL")
	}
	if s.NoNewPrivileges {
		a = append(a, "--security-opt", "no-new-privileges:true")
	}
	if s.PidsLimit > 0 {
		a = append(a, "--pids-limit", strconv.Itoa(s.PidsLimit))
	}
	for _, t := range s.Tmpfs {
		a = append(a, "--tmpfs", t)
	}
	switch {
	case s.NoNetwork:
		a = append(a, "--network", "none")
	case s.Network != "":
		a = append(a, "--network", s.Network)
	}
	for _, e := range s.Env {
		a = append(a, "-e", e[0]+"="+e[1])
	}
	for _, v := range s.Volumes {
		a = append(a, "--mount", mountOpt("type=volume", "dst="+v))
	}
	for _, b := range s.Binds {
		f := []string{"type=bind", "src=" + b.Src, "dst=" + b.Dst}
		if b.ReadOnly {
			f = append(f, "readonly")
		}
		a = append(a, "--mount", mountOpt(f...))
	}
	return append(append(a, s.Image), s.Cmd...)
}

// mountOpt joins --mount fields as CSV, which docker parses, so a path with
// a comma is quoted instead of splitting the option.
func mountOpt(fields ...string) string {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(fields)
	w.Flush()
	return strings.TrimRight(b.String(), "\r\n")
}

func (d *Docker) Create(s backend.Spec) error {
	return run.Check(d.R, &run.Cmd{Args: CreateArgs(s), Stderr: run.Stderr})
}

func (d *Docker) Remove(c string) error { return d.check("rm", "-f", "-v", c) }

func (d *Docker) Stop(c string, timeoutSec int) error {
	return d.check("stop", "-t", strconv.Itoa(timeoutSec), c)
}

func (d *Docker) CopyIn(c, src, dstDir string, keepOwner bool) error {
	args := []string{"cp"}
	if keepOwner {
		args = append(args, "-a")
	}
	return d.check(append(args, "-q", src, c+":"+dstDir)...)
}

func (d *Docker) CopyOut(c, src, hostDir string) error {
	return d.check("cp", "-q", c+":"+src, hostDir)
}

func (d *Docker) StartArgv(c, detachKeys string) []string {
	return []string{"docker", "start", "-a", "-i", "--detach-keys", detachKeys, c}
}

func (d *Docker) AttachArgv(c, detachKeys string) []string {
	return []string{"docker", "attach", "--detach-keys", detachKeys, c}
}

func (d *Docker) ExecArgv(c string, tty bool, detachKeys string, cmd ...string) []string {
	t := "-i"
	if tty {
		t = "-it"
	}
	return append([]string{"docker", "exec", t, "--detach-keys", detachKeys, c}, cmd...)
}

// infoFormat makes docker info print the fields Info needs as JSON.
const infoFormat = `{"OS":{{json .OperatingSystem}},"Version":{{json .ServerVersion}},` +
	`"SecurityOptions":{{json .SecurityOptions}},"Runtimes":{{json .Runtimes}},"DefaultRuntime":{{json .DefaultRuntime}}}`

func (d *Docker) Info() (backend.Info, error) {
	var out bytes.Buffer
	args := []string{"docker", "info", "--format", infoFormat}
	if err := run.Check(d.R, &run.Cmd{Args: args, Stdout: &out}); err != nil {
		return backend.Info{}, err
	}
	return parseInfo(out.Bytes())
}

func parseInfo(data []byte) (backend.Info, error) {
	var raw struct {
		OS, Version, DefaultRuntime string
		SecurityOptions             []string
		Runtimes                    map[string]struct {
			Path string   `json:"path"`
			Args []string `json:"runtimeArgs"`
		}
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return backend.Info{}, fmt.Errorf("can't read docker info: %w", err)
	}
	if raw.Version == "" {
		// some docker CLIs (27) succeed without a daemon, with empty fields
		return backend.Info{}, errors.New("can't reach Docker")
	}
	info := backend.Info{OS: raw.OS, Version: raw.Version, SecurityOptions: raw.SecurityOptions,
		DefaultRuntime: raw.DefaultRuntime, Runtimes: map[string]backend.Runtime{}}
	for name, rt := range raw.Runtimes {
		info.Runtimes[name] = backend.Runtime{Path: rt.Path, Args: rt.Args}
	}
	return info, nil
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
