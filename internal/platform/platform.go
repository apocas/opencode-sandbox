// Package platform checks what this machine supports.
package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

// Platform says how new sandboxes run on this machine.
type Platform struct {
	// AgentOK is false where Docker runs containers in a VM (macOS, Windows,
	// Docker Desktop), under another uid (rootless Docker, userns-remap) or
	// in a runtime that can't connect to a socket here (gVisor without
	// --host-uds): there a socket on this machine can't serve them, so
	// sandboxes get only a disposable key stored inside them, or none.
	AgentOK    bool
	NoAgentWhy string
	// Runtime is the OCI runtime chosen with OPENCODE_SANDBOX_RUNTIME, ""
	// for Docker's default.
	Runtime string
	Info    backend.Info
}

// RuntimeEnv chooses the OCI runtime of new sandboxes, e.g. runsc (gVisor).
const RuntimeEnv = "OPENCODE_SANDBOX_RUNTIME"

// RootRefusal and NoEngine are why Check refuses a setup.
const (
	RootRefusal = "don't run opencode-sandbox as root; sandboxes run with your uid"
	NoEngine    = "can't reach Docker; is it running, and may you use it (e.g. are you in the docker group)?"
	NoEngineMac = "neither Docker nor Apple container is running; start one (Apple container: container system start)"
)

// EngineEnv chooses the engine of new sandboxes: docker or apple.
const EngineEnv = "OPENCODE_SANDBOX_ENGINE"

// ChooseEngine picks the engine for new sandboxes from what's running:
// Apple container if it is (a VM for each sandbox), else Docker, unless
// EngineEnv asks for one.
func ChooseEngine(want string, dockerOK, appleOK bool) (string, error) {
	switch want {
	case "":
		if appleOK {
			return "apple", nil
		}
		if dockerOK {
			return "docker", nil
		}
		return "", errors.New(NoEngine)
	case "docker":
		if dockerOK {
			return "docker", nil
		}
		return "", fmt.Errorf("%s=docker, but %s", EngineEnv, NoEngine)
	case "apple":
		if appleOK {
			return "apple", nil
		}
		return "", fmt.Errorf("%s=apple, but Apple container isn't running here (it needs macOS 26 or later on Apple silicon; start it with: container system start)", EngineEnv)
	}
	return "", fmt.Errorf("%s must be docker or apple, not %q", EngineEnv, want)
}

// AppleWhy is why sandboxes in Apple container get no host ssh-agent.
const AppleWhy = "Apple container runs each sandbox in a VM of its own, which can't reach an ssh-agent on this machine"

// CheckRuntime explains why runtime can't be used for new sandboxes; nil if
// it can.
func CheckRuntime(info backend.Info, name string) error {
	if name == "" {
		return nil
	}
	if _, ok := info.Runtimes[name]; ok {
		return nil
	}
	have := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		have = append(have, name)
	}
	sort.Strings(have)
	return fmt.Errorf("%s=%s, but Docker has no runtime by that name (it has: %s)", RuntimeEnv, name, strings.Join(have, ", "))
}

// Decide finds out whether a host ssh-agent can serve sandboxes that run in
// the OCI runtime chosen ("" for the engine's default).
func Decide(info backend.Info, chosen string) Platform {
	return DecideOn(runtime.GOOS, info, chosen)
}

// DecideOn is Decide on the operating system goos.
func DecideOn(goos string, info backend.Info, chosen string) Platform {
	why := ""
	secOpts := strings.Join(info.SecurityOptions, " ")
	rt := chosen
	if rt == "" {
		rt = info.DefaultRuntime
	}
	switch v := os.Getenv("OPENCODE_SANDBOX_NO_AGENT"); {
	case v != "" && v != "0":
		why = "OPENCODE_SANDBOX_NO_AGENT is set"
	case goos != "linux":
		why = "on " + systemName(goos) + ", Docker runs containers in a VM that can't reach an ssh-agent on this machine"
	case strings.Contains(info.OS, "Docker Desktop"):
		why = "Docker Desktop runs containers in a VM that can't reach an ssh-agent on this machine"
	case strings.Contains(secOpts, "rootless") || strings.Contains(secOpts, "userns"):
		why = "with rootless Docker or userns-remap, sandboxes don't run with your uid, so an ssh-agent on this machine won't serve them"
	case Kind(info, rt) == Gvisor && !HostUDS(info.Runtimes[rt]):
		why = fmt.Sprintf("gVisor (runtime %s) lets sandboxes connect to an ssh-agent on this machine only if Docker starts it with --host-uds=open", rt)
	case Kind(info, rt) == Kata:
		why = "Kata Containers runs sandboxes in VMs that can't reach an ssh-agent on this machine"
	}
	return Platform{AgentOK: why == "", NoAgentWhy: why, Runtime: chosen, Info: info}
}

// Kinds of OCI runtime that change how sandboxes are isolated.
const (
	Gvisor = "gvisor" // runsc: a user-space kernel for each container
	Kata   = "kata"   // a VM for each container
)

// Kind tells what isolation a runtime gives: Gvisor, Kata, or "" for an
// ordinary container runtime (runc, crun…). A runtime the engine doesn't
// know is judged by its name.
func Kind(info backend.Info, name string) string {
	rt, ok := info.Runtimes[name]
	prog := filepath.Base(rt.Path)
	switch {
	case strings.Contains(name, "runsc") || strings.Contains(name, "gvisor") || ok && strings.Contains(prog, "runsc"):
		return Gvisor
	case strings.Contains(name, "kata") || ok && strings.Contains(prog, "kata"):
		return Kata
	}
	return ""
}

// HostUDS is true if a gVisor runtime may connect to sockets on this
// machine (runsc --host-uds=open or =all), which a host ssh-agent needs.
func HostUDS(rt backend.Runtime) bool {
	for i, a := range rt.Args {
		v, ok := strings.CutPrefix(strings.TrimLeft(a, "-"), "host-uds")
		switch {
		case ok && (v == "=open" || v == "=all"):
			return true
		case ok && v == "" && i+1 < len(rt.Args) && (rt.Args[i+1] == "open" || rt.Args[i+1] == "all"):
			return true
		}
	}
	return false
}

// InVM is true where the engine runs containers in a VM rather than on this
// machine's kernel: on any system but Linux, and with Docker Desktop.
func InVM(info backend.Info) bool {
	return runtime.GOOS != "linux" || strings.Contains(info.OS, "Docker Desktop")
}

// SystemName names this operating system ("macOS", "Linux"…).
func SystemName() string { return systemName(runtime.GOOS) }

func systemName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "netbsd":
		return "NetBSD"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return goos
}
