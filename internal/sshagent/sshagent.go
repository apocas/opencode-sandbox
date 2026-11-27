// Package sshagent runs one ssh-agent per open sandbox, holding only that
// sandbox's key; only the agent's socket directory is mounted into the
// sandbox. It drives OpenSSH's own ssh-agent, ssh-add and ssh-keygen.
package sshagent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/state"
	"github.com/apocas/opencode-sandbox/internal/tty"
)

// Agents manages the per-sandbox agents. State reports a sandbox's container
// state, so an agent can be stopped once its sandbox is no longer open.
type Agents struct {
	R     run.Runner
	State func(sandbox string) string
}

// Sock is the agent socket inside dir.
func Sock(dir string) string { return dir + "/sock/ssh-agent.sock" }

func (a Agents) onSock(sock string, stdout *bytes.Buffer, args ...string) int {
	c := &run.Cmd{Args: args, Env: []string{"SSH_AUTH_SOCK=" + sock}}
	if stdout != nil {
		c.Stdout = stdout
	}
	code, err := a.R.Run(c)
	if err != nil {
		return -1
	}
	return code
}

// identities lists the fingerprints the agent at sock holds, one per line
// (or the second word of its "no identities" message).
func (a Agents) identities(sock string) string {
	var out bytes.Buffer
	a.onSock(sock, &out, "ssh-add", "-l")
	var f []string
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if w := strings.Fields(line); len(w) > 1 {
			f = append(f, w[1])
		} else if line != "" {
			f = append(f, "")
		}
	}
	return strings.Join(f, "\n")
}

var agentPID = regexp.MustCompile(`(?m)^SSH_AGENT_PID=([0-9]*);`)

// Ensure (re)starts the agent of sandbox so it holds only key (a private
// key path with its .pub next to it). It may ask for the key's passphrase.
func (a Agents) Ensure(sandbox, key string) error {
	dir := state.AgentDir(sandbox)
	sock := Sock(dir)
	if !readable(key) || !readable(key+".pub") {
		return fmt.Errorf("missing key %s (or its .pub)", key)
	}
	want, err := Fingerprint(a.R, key+".pub")
	if err != nil {
		return err
	}
	if err := state.EnsureRunDir(); err != nil {
		return err
	}
	if err := state.MkdirPrivate(state.RunDir()+"/agents", dir, dir+"/sock"); err != nil {
		return err
	}
	if err := os.WriteFile(dir+"/key", []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now) // marks it fresh for GC

	if a.onSock(sock, nil, "ssh-add", "-l") == 2 { // nothing listening: start a fresh agent
		_ = os.Remove(sock)
		// -P: refuse to load any PKCS#11/FIDO provider library on request.
		out, err := run.Output(a.R, "ssh-agent", "-s", "-a", sock, "-P", "/nonexistent")
		if err != nil {
			return err
		}
		pid := ""
		if m := agentPID.FindStringSubmatch(out); m != nil {
			pid = m[1] + "\n"
		}
		if err := os.WriteFile(dir+"/ssh-agent.pid", []byte(pid), 0o600); err != nil {
			return err
		}
	}

	if a.identities(sock) == want {
		return nil
	}
	a.onSock(sock, nil, "ssh-add", "-D")
	load := &run.Cmd{Args: []string{"ssh-add", "-q", key}, Env: []string{"SSH_AUTH_SOCK=" + sock}, Stdout: run.Stderr, Stderr: run.Stderr}
	if tty.StdinIsTerminal() {
		load.Stdin = os.Stdin // may ask for the key's passphrase
	}
	if code, err := a.R.Run(load); err != nil || code != 0 {
		return fmt.Errorf("could not load %s", key)
	}
	if a.identities(sock) != want {
		return fmt.Errorf("agent for '%s' holds unexpected identities; run: opencode-sandbox --stop-agent", sandbox)
	}
	return nil
}

// Stop kills the agent in dir and removes dir.
func Stop(dir string) {
	KillPidFile(dir + "/ssh-agent.pid")
	_ = os.RemoveAll(dir)
}

// KillPidFile stops the process whose pid the file holds, if any.
func KillPidFile(path string) {
	if b, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
}

// Close stops a sandbox's agent unless the sandbox is (still) open.
func (a Agents) Close(sandbox string) {
	if a.State(sandbox) != "running" {
		Stop(state.AgentDir(sandbox))
	}
}

// GC stops agents whose sandbox is no longer open (e.g. the launcher was
// killed outright). Fresh ones are skipped: their sandbox may still be
// starting.
func (a Agents) GC() {
	entries, err := os.ReadDir(state.RunDir() + "/agents")
	if err != nil {
		return
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.IsDir() || time.Since(fi.ModTime()) <= time.Minute {
			continue
		}
		a.Close(e.Name())
	}
}

// Fingerprint is the SHA256 fingerprint of a key file.
func Fingerprint(r run.Runner, keyFile string) (string, error) {
	out, err := run.Output(r, "ssh-keygen", "-lf", keyFile)
	if err != nil {
		return "", err
	}
	if w := strings.Fields(out); len(w) > 1 {
		return w[1], nil
	}
	return "", nil
}

// Describe is how the key menu shows a key: type, fingerprint and comment,
// e.g. "(ED25519) SHA256:abc… me@laptop".
func Describe(r run.Runner, pubFile string) string {
	var out bytes.Buffer
	_, _ = r.Run(&run.Cmd{Args: []string{"ssh-keygen", "-lf", pubFile}, Stdout: &out, Stderr: run.Stderr})
	w := strings.Fields(strings.SplitN(out.String(), "\n", 2)[0])
	if len(w) < 2 {
		return ""
	}
	comment := ""
	for _, c := range w[2 : len(w)-1] {
		comment += " " + c
	}
	return fmt.Sprintf("%-9s %s%s", w[len(w)-1], w[1], comment)
}

// Comment is the comment of a key file ("" if it can't be read).
func Comment(r run.Runner, keyFile string) string {
	var out bytes.Buffer
	if code, err := r.Run(&run.Cmd{Args: []string{"ssh-keygen", "-lf", keyFile}, Stdout: &out}); err != nil || code != 0 {
		return ""
	}
	w := strings.Fields(strings.SplitN(out.String(), "\n", 2)[0])
	if len(w) < 4 {
		return ""
	}
	return strings.Join(w[2:len(w)-1], " ")
}

// AgentDirs lists the agent directories that exist, in name order.
func AgentDirs() []string {
	entries, _ := os.ReadDir(state.RunDir() + "/agents")
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(state.RunDir(), "agents", e.Name()))
		}
	}
	return dirs
}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}
