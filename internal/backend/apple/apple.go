// Package apple implements backend.Engine with Apple's container CLI
// (https://github.com/apple/container), which runs every container in a
// lightweight VM of its own.
//
// Where it differs from docker, as of container 1.7:
//   - Only folders can be bind-mounted, not single files.
//   - A running container can't be attached to, and a terminal can't detach.
//   - `container cp` needs a running container and doesn't see its mounted
//     volumes, so files are streamed as tar through `container exec`, or
//     through a short-lived helper container for a stopped one.
//   - A new volume is empty and owned by root (docker fills it from the
//     image), so Create prepares volumes with a helper.
//   - Containers on one network can reach each other, so every sandbox gets
//     a network of its own.
//   - No no-new-privileges or PID limit; the VM boundary stands in.
package apple

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/tarx"
)

// Memory and CPUs of a sandbox's VM (container's defaults are 1 GiB and 4).
const (
	Memory = "4G"
	CPUs   = 4
)

// Apple drives the container CLI found on PATH.
type Apple struct{ R run.Runner }

var _ backend.Engine = (*Apple)(nil)

func (a *Apple) Name() string { return "apple" }

func (a *Apple) Caps() backend.Caps { return backend.Caps{VMPerContainer: true} }

// Available reports whether the container CLI is installed, on a macOS
// with the networks sandboxes need (26 or later), and its services run.
func Available(r run.Runner) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if _, err := exec.LookPath("container"); err != nil {
		return false
	}
	var v bytes.Buffer
	if code, err := r.Run(&run.Cmd{Args: []string{"sw_vers", "-productVersion"}, Stdout: &v}); err != nil || code != 0 {
		return false
	}
	if major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(v.String()), ".", 2)[0]); err != nil || major < 26 {
		return false
	}
	return run.Quiet(r, "container", "system", "status") == 0
}

func (a *Apple) cmd(args ...string) []string { return append([]string{"container"}, args...) }

func (a *Apple) output(args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := run.Check(a.R, &run.Cmd{Args: a.cmd(args...), Stdout: &out}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// check runs container with stdout discarded and stderr shown.
func (a *Apple) check(args ...string) error {
	return run.Check(a.R, &run.Cmd{Args: a.cmd(args...), Stderr: run.Stderr})
}

func (a *Apple) quiet(args ...string) int { return run.Quiet(a.R, a.cmd(args...)...) }

// container is what inspect and list say about a container.
type container struct {
	ID            string `json:"id"`
	Configuration struct {
		Labels       map[string]string `json:"labels"`
		CreationDate time.Time         `json:"creationDate"`
		Image        struct {
			Reference string `json:"reference"`
		} `json:"image"`
		Mounts []struct {
			Destination string                     `json:"destination"`
			Source      string                     `json:"source"`
			Type        map[string]json.RawMessage `json:"type"`
		} `json:"mounts"`
		Networks []struct {
			Network string `json:"network"`
		} `json:"networks"`
	} `json:"configuration"`
	Status struct {
		State       string     `json:"state"`
		StartedDate *time.Time `json:"startedDate"`
	} `json:"status"`
}

func (c *container) volumes() (names, dsts []string) {
	for _, m := range c.Configuration.Mounts {
		if raw, ok := m.Type["volume"]; ok {
			var v struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &v) == nil && v.Name != "" {
				names, dsts = append(names, v.Name), append(dsts, m.Destination)
			}
		}
	}
	return names, dsts
}

func (a *Apple) inspect(c string) (*container, error) {
	out, err := a.output("inspect", c)
	if err != nil {
		return nil, err
	}
	var cs []container
	if err := json.Unmarshal(out, &cs); err != nil || len(cs) != 1 {
		return nil, fmt.Errorf("can't read what container says about %s", c)
	}
	return &cs[0], nil
}

func (a *Apple) Exists(c string) bool { return a.quiet("inspect", c) == 0 }

func (a *Apple) State(c string) string {
	ct, err := a.inspect(c)
	if err != nil {
		return ""
	}
	if ct.Status.State == "stopped" {
		return "exited" // as docker says
	}
	return ct.Status.State
}

func (a *Apple) Label(c, label string) (string, error) {
	ct, err := a.inspect(c)
	if err != nil {
		return "", err
	}
	return ct.Configuration.Labels[label], nil
}

func (a *Apple) list(label string) ([]container, error) {
	out, err := a.output("list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var all, cs []container
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, errors.New("can't read container's list of containers")
	}
	for _, c := range all {
		if _, ok := c.Configuration.Labels[label]; ok {
			cs = append(cs, c)
		}
	}
	return cs, nil
}

func (a *Apple) Names(label string) ([]string, error) {
	cs, err := a.list(label)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Configuration.Labels[label]
	}
	return names, nil
}

func (a *Apple) Rows(label string) ([]backend.Row, error) {
	cs, err := a.list(label)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows := make([]backend.Row, len(cs))
	for i, c := range cs {
		l := func(k string) string { return c.Configuration.Labels["opencode-sandbox."+k] }
		rows[i] = backend.Row{
			Name: c.Configuration.Labels[label], KeyKind: l("keykind"), Key: l("key"),
			Status:  status(c, now),
			Created: HumanDuration(now.Sub(c.Configuration.CreationDate)) + " ago",
			Source:  l("source"), Providers: l("providers"), KeyMode: l("keymode"), Runtime: l("runtime"),
			Engine: "apple", Version: l("version"),
		}
	}
	return rows, nil
}

// status describes a container's state the way docker ps does.
func status(c container, now time.Time) string {
	switch {
	case c.Status.State == "running" && c.Status.StartedDate != nil:
		return "Up " + HumanDuration(now.Sub(*c.Status.StartedDate))
	case c.Status.State == "running":
		return "Up"
	case c.Status.StartedDate == nil:
		return "Created"
	}
	return "Exited"
}

// HumanDuration is how docker says how long ago something happened.
func HumanDuration(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 1:
		return "Less than a second"
	case s == 1:
		return "1 second"
	case s < 60:
		return fmt.Sprintf("%d seconds", s)
	}
	m := int(d.Minutes())
	switch {
	case m == 1:
		return "About a minute"
	case m < 60:
		return fmt.Sprintf("%d minutes", m)
	}
	h := int(d.Hours() + 0.5)
	switch {
	case h == 1:
		return "About an hour"
	case h < 48:
		return fmt.Sprintf("%d hours", h)
	case h < 24*7*2:
		return fmt.Sprintf("%d days", h/24)
	case h < 24*30*2:
		return fmt.Sprintf("%d weeks", h/24/7)
	case h < 24*365*2:
		return fmt.Sprintf("%d months", h/24/30)
	}
	return fmt.Sprintf("%d years", int(d.Hours())/24/365)
}

func (a *Apple) BindSources(c string) ([]string, error) {
	ct, err := a.inspect(c)
	if err != nil {
		return nil, err
	}
	var srcs []string
	for _, m := range ct.Configuration.Mounts {
		if _, ok := m.Type["virtiofs"]; ok {
			srcs = append(srcs, m.Source)
		}
	}
	return srcs, nil
}

func (a *Apple) ImageExists(ref string) bool { return a.quiet("image", "inspect", ref) == 0 }

func (a *Apple) Build(ref, contextDir string, buildArgs [][2]string, extra []string, out io.Writer) error {
	args := a.cmd("build", "-t", ref)
	for _, b := range buildArgs {
		args = append(args, "--build-arg", b[0]+"="+b[1])
	}
	args = append(append(args, extra...), contextDir)
	return run.Check(a.R, &run.Cmd{Args: append(args[:2:2], append([]string{"--progress", "plain"}, args[2:]...)...), Stdout: out, Stderr: out})
}

// EnsureNetwork does nothing: Create gives every container a network of its
// own, since container has no way to keep containers on one network apart.
func (a *Apple) EnsureNetwork(string) error { return nil }

// Label keys marking a sandbox's own network and volumes, so they're never
// taken for sandboxes and can be found if a container goes missing.
const (
	NetworkFor = "opencode-sandbox.network-for"
	VolumeFor  = "opencode-sandbox.volume-for"
)

// volumeName names the volume a container mounts at dst.
func volumeName(c, dst string) string {
	return c + "-" + strings.ReplaceAll(strings.Trim(dst, "/"), "/", "-")
}

// CreateArgs is the container command that creates s, given its network and
// volume names.
func CreateArgs(s backend.Spec, network string) ([]string, error) {
	args := []string{"container", "create", "-i"}
	if s.TTY {
		args = append(args, "-t")
	}
	args = append(args, "--name", s.Container)
	for _, l := range s.Labels {
		args = append(args, "--label", l[0]+"="+l[1])
	}
	if s.Init {
		args = append(args, "--init")
	}
	if s.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	if s.DropAllCaps {
		args = append(args, "--cap-drop", "ALL")
	}
	args = append(args, "--memory", Memory, "--cpus", fmt.Sprint(CPUs))
	for _, t := range s.Tmpfs {
		args = append(args, "--tmpfs", t)
	}
	args = append(args, "--network", network)
	for _, e := range s.Env {
		args = append(args, "-e", e[0]+"="+e[1])
	}
	for _, v := range s.Volumes {
		args = append(args, "-v", volumeName(s.Container, v)+":"+v)
	}
	for _, b := range s.Binds {
		if strings.ContainsAny(b.Src+b.Dst, ",=") {
			return nil, fmt.Errorf("Apple container can't mount %s: its path has a comma or =", b.Src)
		}
		m := "type=bind,source=" + b.Src + ",target=" + b.Dst
		if b.ReadOnly {
			m += ",readonly"
		}
		args = append(args, "--mount", m)
	}
	return append(append(args, s.Image), s.Cmd...), nil
}

// Create creates the container with a network and volumes of its own. The
// volumes start as docker's would: with what the image has at their path,
// owned as in the image.
func (a *Apple) Create(s backend.Spec) error {
	if s.Runtime != "" {
		return fmt.Errorf("Apple container has no OCI runtimes to choose from (%s)", s.Runtime)
	}
	args, err := CreateArgs(s, s.Container)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			a.removeParts(s.Container, s.Volumes)
		}
	}()
	a.removeParts(s.Container, s.Volumes) // left over from an earlier failure
	if err := a.check("network", "create", "--label", NetworkFor+"="+s.Container, s.Container); err != nil {
		return err
	}
	helper := a.cmd("run", "--rm", "--progress", "none", "--user", "0", "--network", s.Container, "--entrypoint", "sh")
	script := ""
	for i, dst := range s.Volumes {
		vol := volumeName(s.Container, dst)
		if err := a.check("volume", "create", "--label", VolumeFor+"="+s.Container, vol); err != nil {
			return err
		}
		mnt := fmt.Sprintf("/mnt/v%d", i)
		helper = append(helper, "-v", vol+":"+mnt)
		q := run.Quote([]string{dst})
		script += fmt.Sprintf("cp -a %s/. %s/ && chown $(stat -c %%u:%%g %s) %s && chmod $(stat -c %%a %s) %s && rm -rf %s/lost+found && ",
			q, mnt, q, mnt, q, mnt, mnt)
	}
	if script != "" {
		helper = append(helper, s.Image, "-c", script+"true")
		if err := run.Check(a.R, &run.Cmd{Args: helper, Stderr: run.Stderr}); err != nil {
			return err
		}
	}
	if err := run.Check(a.R, &run.Cmd{Args: args, Stderr: run.Stderr}); err != nil {
		return err
	}
	done = true
	return nil
}

// removeParts deletes a container's network and volumes, if there.
func (a *Apple) removeParts(c string, dsts []string) {
	for _, dst := range dsts {
		_ = a.quiet("volume", "delete", volumeName(c, dst))
	}
	_ = a.quiet("network", "delete", c)
}

func (a *Apple) Remove(c string) error {
	ct, err := a.inspect(c)
	if err != nil {
		return err
	}
	if err := a.check("delete", "--force", c); err != nil {
		return err
	}
	vols, _ := ct.volumes()
	for _, v := range vols {
		_ = a.quiet("volume", "delete", v)
	}
	for _, n := range ct.Configuration.Networks {
		if n.Network == c {
			_ = a.quiet("network", "delete", n.Network)
		}
	}
	return nil
}

func (a *Apple) Stop(c string, timeoutSec int) error {
	return a.check("stop", "-t", fmt.Sprint(timeoutSec), c)
}

// helperArgs runs a short-lived container with c's volumes at their places,
// as the image's user, to reach the files of a stopped container.
func (a *Apple) helperArgs(ct *container, interactive bool, cmd ...string) []string {
	args := a.cmd("run", "--rm", "--progress", "none", "--network", ct.ID)
	if interactive {
		args = append(args, "-i")
	}
	vols, dsts := ct.volumes()
	for i := range vols {
		args = append(args, "-v", vols[i]+":"+dsts[i])
	}
	args = append(args, "--entrypoint", cmd[0], ct.Configuration.Image.Reference)
	return append(args, cmd[1:]...)
}

// CopyIn streams src as tar into the container: through exec if it's
// running, else through a helper. Files end up owned by the image's user
// either way, which is what keepOwner asks for.
func (a *Apple) CopyIn(c, src, dstDir string, keepOwner bool) error {
	ct, err := a.inspect(c)
	if err != nil {
		return err
	}
	tarCmd := []string{"tar", "-C", dstDir, "-xf", "-", "--no-same-owner"}
	args := a.cmd(append([]string{"exec", "-i", c}, tarCmd...)...)
	if ct.Status.State != "running" {
		args = a.helperArgs(ct, true, tarCmd...)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarx.Write(pw, src)) }()
	err = run.Check(a.R, &run.Cmd{Args: args, Stdin: pr, Stderr: run.Stderr})
	pr.Close()
	return err
}

// CopyOut streams src out of the container as tar and unpacks it into
// hostDir, never overwriting and never writing outside it.
func (a *Apple) CopyOut(c, src, hostDir string) error {
	ct, err := a.inspect(c)
	if err != nil {
		return err
	}
	src = path.Clean(src)
	tarCmd := []string{"tar", "-C", path.Dir(src), "-cf", "-", path.Base(src)}
	args := a.cmd(append([]string{"exec", c}, tarCmd...)...)
	if ct.Status.State != "running" {
		args = a.helperArgs(ct, false, tarCmd...)
	}
	pr, pw := io.Pipe()
	extracted := make(chan error, 1)
	go func() {
		err := tarx.Extract(pr, hostDir)
		_, _ = io.Copy(io.Discard, pr) // let the sender finish
		extracted <- err
	}()
	err = run.Check(a.R, &run.Cmd{Args: args, Stdout: pw, Stderr: run.Stderr})
	pw.Close()
	if xerr := <-extracted; err == nil {
		err = xerr
	}
	return err
}

func (a *Apple) StartArgv(c, _ string) []string {
	return a.cmd("start", "--attach", "--interactive", c)
}

// AttachArgv is nil: container can't attach to a running container.
func (a *Apple) AttachArgv(string, string) []string { return nil }

func (a *Apple) ExecArgv(c string, tty bool, _ string, cmd ...string) []string {
	args := a.cmd("exec", "-i")
	if tty {
		args = append(args, "-t")
	}
	return append(append(args, c), cmd...)
}

func (a *Apple) Info() (backend.Info, error) {
	out, err := a.output("system", "status", "--format", "json")
	if err != nil {
		return backend.Info{}, err
	}
	var st struct {
		Status string `json:"status"`
		Server struct {
			Version string `json:"version"`
		} `json:"server"`
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Status != "running" {
		return backend.Info{}, errors.New("Apple container isn't running (start it with: container system start)")
	}
	return backend.Info{OS: "Apple container", Version: st.Server.Version}, nil
}
