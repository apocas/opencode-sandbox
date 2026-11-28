// Package health reports whether sandboxes can run on this machine, how a
// new one is isolated, and which stronger isolation could be installed.
package health

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/gateway"
	"github.com/apocas/opencode-sandbox/internal/occonfig"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// Facts is what the report is made from; Gather finds them out.
type Facts struct {
	Version string // opencode-sandbox's
	GOOS    string
	Machine string // GOARCH: "amd64", "arm64"…
	System  string // "macOS 27.2.1", "Ubuntu 24.04.1 LTS"…
	Arch    string // "Apple silicon", "Intel", "x86_64", "arm64"…
	// MacOS is the major macOS version (0 elsewhere or if unknown);
	// AppleSilicon is true on an arm64 Mac, even under Rosetta.
	MacOS        int
	AppleSilicon bool
	Root         bool
	Home         string

	DockerCLI bool          // docker is on PATH
	Docker    *backend.Info // nil when Docker can't be reached
	InVM      bool          // Docker runs containers in a VM

	AppleCLI     bool   // Apple's container CLI is on PATH (macOS)
	AppleVersion string // "1.7.1"
	AppleRunning bool

	// Engine is where new sandboxes go: "docker", "apple", or "" if
	// nowhere (EngineErr says why). EngineEnv is what
	// OPENCODE_SANDBOX_ENGINE asks for.
	Engine    string
	EngineErr string
	EngineEnv string
	// Agent is how new sandboxes get SSH keys.
	Agent platform.Platform
	// Runtime is the OCI runtime of new sandboxes in Docker ("" for Docker's
	// default): the one OPENCODE_SANDBOX_RUNTIME asks for (RuntimeEnv), or
	// gVisor, used because Docker has it (RuntimeAuto). RuntimeErr says why
	// the one asked for can't be used.
	Runtime     string
	RuntimeAuto bool
	RuntimeEnv  string
	RuntimeErr  string
	// ImageOK: the new sandboxes' engine has the image.
	Image   string
	ImageOK bool

	Config    string
	ConfigErr string // why the config can't be used; "" if it can
	Providers int
	// Gateway: new sandboxes reach their providers through a gateway, which
	// keeps the API keys on this machine. KeysInside are the providers with
	// a secret that goes into a sandbox all the same, and why.
	Gateway    bool
	KeysInside []KeyInside
	// MCPKept is how many MCP servers of your opencode config have secrets
	// the gateway keeps (their headers, their address); MCPInside are the
	// ones with a secret that goes into a sandbox all the same, and why.
	MCPKept   int
	MCPInside []KeyInside
	// NoNetwork: new sandboxes get no network (OPENCODE_SANDBOX_NETWORK is
	// none), and reach only their providers, through the gateway.
	NoNetwork bool
	// Filtered: new sandboxes get a filtered network (it is "filtered"),
	// and reach their providers and the hosts on their lists.
	Filtered bool

	MissingSSH []string // OpenSSH tools not on PATH
	PidsLimit  int
	// Limits says the memory and CPUs a new sandbox may use: "4 GB of
	// memory, 4 CPUs". LimitsErr is why what the environment asks for
	// (OPENCODE_SANDBOX_MEMORY, _CPUS) can't be used.
	Limits    string
	LimitsErr string

	RunscPath string // where gVisor's runsc is installed, if it is (Linux)
	Systemd   bool   // Docker is a systemd service here (Linux)
}

// appleOK: new sandboxes can go to Apple container.
func (f Facts) appleOK() bool { return f.AppleRunning && f.AppleSilicon && f.MacOS >= 26 }

// KeyInside is a provider (or an MCP server) with a secret the gateway
// doesn't keep out of sandboxes, and why.
type KeyInside struct{ Provider, Why string }

// Gather finds out the facts on this machine, with the docker and apple
// engines. It only reads: nothing is built, started or changed.
func Gather(r run.Runner, docker, apple backend.Engine, version, image string) Facts {
	f := Facts{Version: version, GOOS: runtime.GOOS, Machine: runtime.GOARCH, Root: os.Getuid() == 0, Home: state.Home(), Image: image}
	f.System, f.Arch = platform.SystemName(), goarch(runtime.GOARCH)
	switch f.GOOS {
	case "darwin":
		if v, ok := output(r, "sw_vers", "-productVersion"); ok {
			f.System = "macOS " + v
			f.MacOS, _ = strconv.Atoi(strings.SplitN(v, ".", 2)[0])
		}
		v, _ := output(r, "sysctl", "-n", "hw.optional.arm64")
		f.AppleSilicon = v == "1"
		f.Arch = "Intel"
		if f.AppleSilicon {
			f.Arch = "Apple silicon"
		}
		if _, err := exec.LookPath("container"); err == nil {
			if v, ok := output(r, "container", "--version"); ok && strings.Contains(strings.ToLower(v), "container") {
				f.AppleCLI = true
				f.AppleVersion = appleVersion(v)
				f.AppleRunning = run.Quiet(r, "container", "system", "status") == 0
			}
		}
	case "linux":
		if name := osRelease("/etc/os-release"); name != "" {
			f.System = name
		}
		// Also where setup puts it, which root's PATH may lack.
		if p, err := exec.LookPath("runsc"); err == nil {
			f.RunscPath = p
		} else if fi, err := os.Stat("/usr/local/bin/runsc"); err == nil && fi.Mode().IsRegular() {
			f.RunscPath = "/usr/local/bin/runsc"
		}
		f.Systemd = run.Quiet(r, "systemctl", "is-active", "--quiet", "docker") == 0
	}

	if _, err := exec.LookPath("docker"); err == nil {
		f.DockerCLI = true
		if info, err := docker.Info(); err == nil {
			info = platform.CheckRuntimes(info)
			f.Docker = &info
			f.InVM = platform.InVM(info)
		}
	}

	f.EngineEnv, f.RuntimeEnv = os.Getenv(platform.EngineEnv), os.Getenv(platform.RuntimeEnv)
	name, err := platform.ChooseEngine(f.EngineEnv, f.Docker != nil, f.appleOK())
	if err != nil {
		f.EngineErr = err.Error()
	}
	f.Engine = name
	rt := f.RuntimeEnv
	switch name {
	case "docker":
		f.Runtime, f.RuntimeAuto = platform.ChooseRuntime(f.GOOS, *f.Docker, rt)
		if err := platform.CheckRuntime(*f.Docker, f.Runtime); err != nil {
			f.RuntimeErr, f.Runtime = err.Error(), ""
		}
		f.Agent = platform.Decide(*f.Docker, f.Runtime)
		f.ImageOK = docker.ImageExists(image)
	case "apple":
		f.Agent = platform.Platform{NoAgentWhy: platform.AppleWhy}
		if rt != "" {
			f.RuntimeErr = platform.RuntimeEnv + "=" + rt + " chooses a Docker runtime, but new sandboxes go to Apple container; set " + platform.EngineEnv + "=docker to use Docker"
		}
		f.ImageOK = apple.ImageExists(image)
	}

	f.Config = state.Config()
	switch root, err := occonfig.Load(f.Config); {
	case !readable(f.Config):
		f.ConfigErr = "missing"
	case err != nil || root == nil || root == false:
		f.ConfigErr = "not JSON"
	default:
		ids := occonfig.ProviderIDs(root)
		f.Providers = len(ids)
		if f.Providers == 0 {
			f.ConfigErr = "no providers"
		}
		env := gateway.Env{Getenv: os.Getenv, Dir: filepath.Dir(f.Config), Home: state.Home()}
		_, inside := gateway.Routes(root, ids, env, false)
		for _, id := range ids {
			if why, ok := inside[id]; ok {
				f.KeysInside = append(f.KeysInside, KeyInside{Provider: id, Why: why})
			}
		}
		servers, inside := gateway.MCP(root, env, false)
		f.MCPKept = len(servers)
		for name, why := range inside {
			f.MCPInside = append(f.MCPInside, KeyInside{Provider: name, Why: why})
		}
		sort.Slice(f.MCPInside, func(i, j int) bool { return f.MCPInside[i].Provider < f.MCPInside[j].Provider })
	}
	f.Gateway = gateway.Wanted(os.Getenv(gateway.Switch))
	f.NoNetwork = os.Getenv("OPENCODE_SANDBOX_NETWORK") == "none"
	f.Filtered = os.Getenv("OPENCODE_SANDBOX_NETWORK") == "filtered"

	for _, tool := range []string{"ssh-keygen", "ssh-agent", "ssh-add"} {
		if _, err := exec.LookPath(tool); err != nil {
			f.MissingSSH = append(f.MissingSSH, tool)
		}
	}
	return f
}

// appleVersion finds the version in container --version's "container CLI
// version 1.7.1 (build: …)".
func appleVersion(v string) string {
	fields := strings.Fields(strings.SplitN(v, "\n", 2)[0])
	for i, w := range fields {
		if w == "version" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return strings.Join(fields, " ")
}

// output runs a command with stdin empty and stderr discarded, and returns
// its stdout without trailing newlines.
func output(r run.Runner, args ...string) (string, bool) {
	var out bytes.Buffer
	code, err := r.Run(&run.Cmd{Args: args, Stdout: &out})
	if err != nil || code != 0 {
		return "", false
	}
	return strings.TrimRight(out.String(), "\n"), true
}

func osRelease(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			if u, err := strconv.Unquote(v); err == nil {
				return u
			}
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func goarch(a string) string {
	switch a {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	}
	return a
}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// runtimeOf is platform.RuntimeOf where Docker may be out of reach.
func runtimeOf(info *backend.Info, kind string) string {
	if info == nil {
		return ""
	}
	return platform.RuntimeOf(*info, kind)
}

// used is the runtime new sandboxes run in.
func (f Facts) used() string {
	if f.Runtime != "" || f.Docker == nil {
		return f.Runtime
	}
	return f.Docker.DefaultRuntime
}
