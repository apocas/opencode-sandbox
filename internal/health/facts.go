// Package health reports whether sandboxes can run on this machine, how a
// new one is isolated, and which stronger isolation could be installed.
package health

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/occonfig"
	"github.com/apocas/opencode-sandbox/internal/platform"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// Facts is what the report is made from; Gather finds them out.
type Facts struct {
	Version string // opencode-sandbox's
	GOOS    string
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
	// nowhere (EngineErr says why).
	Engine    string
	EngineErr string
	// Agent is how new sandboxes get SSH keys.
	Agent platform.Platform
	// Runtime is the OCI runtime chosen with OPENCODE_SANDBOX_RUNTIME ("" for
	// Docker's default); RuntimeErr says why it can't be used.
	Runtime    string
	RuntimeErr string
	// ImageOK: the new sandboxes' engine has the image.
	Image   string
	ImageOK bool

	Config    string
	ConfigErr string // why the config can't be used; "" if it can
	Providers int

	MissingSSH []string // OpenSSH tools not on PATH
	PidsLimit  int

	RunscOnPath bool // gVisor's runsc is installed (Linux)
}

// appleOK: new sandboxes can go to Apple container.
func (f Facts) appleOK() bool { return f.AppleRunning && f.AppleSilicon && f.MacOS >= 26 }

// Gather finds out the facts on this machine, with the docker and apple
// engines. It only reads: nothing is built, started or changed.
func Gather(r run.Runner, docker, apple backend.Engine, version, image string) Facts {
	f := Facts{Version: version, GOOS: runtime.GOOS, Root: os.Getuid() == 0, Home: state.Home(), Image: image}
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
		_, err := exec.LookPath("runsc")
		f.RunscOnPath = err == nil
	}

	if _, err := exec.LookPath("docker"); err == nil {
		f.DockerCLI = true
		if info, err := docker.Info(); err == nil {
			f.Docker = &info
			f.InVM = platform.InVM(info)
		}
	}

	name, err := platform.ChooseEngine(os.Getenv(platform.EngineEnv), f.Docker != nil, f.appleOK())
	if err != nil {
		f.EngineErr = err.Error()
	}
	f.Engine = name
	rt := os.Getenv(platform.RuntimeEnv)
	switch name {
	case "docker":
		f.Runtime = rt
		if err := platform.CheckRuntime(*f.Docker, rt); err != nil {
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
		f.Providers = len(occonfig.ProviderIDs(root))
		if f.Providers == 0 {
			f.ConfigErr = "no providers"
		}
	}

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

// runtimeOf finds the engine's runtime of a kind (platform.Gvisor,
// platform.Kata), preferring the default one, then one that can reach an
// ssh-agent on this machine.
func runtimeOf(info *backend.Info, kind string) string {
	if info == nil {
		return ""
	}
	if platform.Kind(*info, info.DefaultRuntime) == kind {
		return info.DefaultRuntime
	}
	var names []string
	for name := range info.Runtimes {
		if platform.Kind(*info, name) == kind {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := platform.HostUDS(info.Runtimes[names[i]]), platform.HostUDS(info.Runtimes[names[j]])
		if a != b {
			return a
		}
		return names[i] < names[j]
	})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// used is the runtime new sandboxes run in.
func (f Facts) used() string {
	if f.Runtime != "" || f.Docker == nil {
		return f.Runtime
	}
	return f.Docker.DefaultRuntime
}
