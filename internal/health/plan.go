package health

import (
	"fmt"
	"path/filepath"

	"github.com/apocas/opencode-sandbox/internal/platform"
)

// Plan is what `opencode-sandbox setup` does on this machine, so that new
// sandboxes get the strongest isolation it can have.
type Plan struct {
	// What is set up: "Apple container" or "gVisor"; "" if nothing is.
	What string
	// Gain is what new sandboxes get from it.
	Gain string
	// Steps to take, in order. Where there are none, Note says why.
	Steps []Step
	// Best: there's nothing to do because new sandboxes already get the
	// strongest isolation this machine can have.
	Best bool
	Note string
	// After is what's left for you to do once it's set up.
	After string
}

// Step is one step of a Plan.
type Step struct {
	Title string
	// Fetch is what the step downloads and checks: FetchApple, FetchGvisor
	// or "".
	Fetch string
	// Cmds are the commands the step runs, as shown and as run. In an
	// argument, Downloads stands for the folder of what was fetched.
	Cmds [][]string
}

const (
	// FetchApple downloads Apple's installer for container as ApplePkg,
	// and checks that Apple signed it.
	FetchApple = "apple"
	// FetchGvisor downloads gVisor's latest release, checks it against its
	// published SHA-512, and unpacks it: runsc, containerd-shim-runsc-v1
	// and the folder gvisor-bin.
	FetchGvisor = "gvisor"
	Downloads   = "{downloads}"
	ApplePkg    = "container-installer.pkg"
)

// Sudo is true if a step of the plan needs root.
func (p Plan) Sudo() bool {
	for _, s := range p.Steps {
		for _, c := range s.Cmds {
			if c[0] == "sudo" {
				return true
			}
		}
	}
	return false
}

// PlanFor decides what setup would do, from the facts.
func PlanFor(f Facts) Plan {
	switch f.GOOS {
	case "darwin":
		return applePlan(f)
	case "linux":
		return gvisorPlan(f)
	}
	return Plan{Note: "there's nothing opencode-sandbox can set up on " + f.System}
}

func applePlan(f Facts) Plan {
	const vm = "Apple container, which gives each sandbox a VM of its own, "
	// What this Mac has instead.
	instead := " Without it, sandboxes need Docker: https://docs.docker.com/get-started/get-docker/"
	if f.Docker != nil {
		instead = " Sandboxes here share " + vmName(f) + " with your other containers, and that's the most this Mac can do."
	}
	switch {
	case !f.AppleSilicon:
		return Plan{Note: vm + "needs a Mac with Apple silicon." + instead}
	case f.MacOS > 0 && f.MacOS < 26:
		return Plan{Note: fmt.Sprintf("%sneeds macOS 26 or later (this is %s).%s", vm, f.System, instead)}
	case f.Engine == "apple":
		return Plan{Best: true, Note: "each new sandbox already gets a VM of its own (Apple container " + f.AppleVersion + ")"}
	case f.AppleRunning && f.EngineEnv != "":
		return Plan{Note: fmt.Sprintf("Apple container %s is running, but %s=%s keeps new sandboxes out of it; unset it, and each gets a VM of its own.",
			f.AppleVersion, platform.EngineEnv, f.EngineEnv)}
	case f.AppleRunning:
		return Plan{Note: "Apple container " + f.AppleVersion + " is running, but new sandboxes can't go to it; `opencode-sandbox health` says why."}
	}
	p := Plan{What: "Apple container", Gain: "each new sandbox gets a lightweight VM of its own"}
	if f.Docker != nil {
		p.Gain += ", instead of sharing one Linux VM with all your containers"
	}
	start := "(the first start downloads the Linux kernel its VMs run)"
	if f.AppleCLI {
		p.Steps = []Step{{Title: "Start Apple container " + f.AppleVersion + " " + start,
			Cmds: [][]string{{"container", "system", "start", "--enable-kernel-install"}}}}
	} else {
		p.Steps = []Step{
			{Title: "Download Apple's installer for container from github.com/apple/container (about 170 MB) and check that Apple signed it", Fetch: FetchApple},
			{Title: "Install it, in /usr/local", Cmds: [][]string{{"sudo", "/usr/sbin/installer", "-pkg", Downloads + "/" + ApplePkg, "-target", "/"}}},
			{Title: "Start it " + start, Cmds: [][]string{{"/usr/local/bin/container", "system", "start", "--enable-kernel-install"}}},
		}
	}
	if f.EngineEnv == "docker" {
		p.After = platform.EngineEnv + "=docker is set, which keeps new sandboxes in Docker: unset it to use Apple container."
	}
	return p
}

func gvisorPlan(f Facts) Plan {
	const own = "gVisor, a kernel of its own for each sandbox, "
	switch {
	case f.Docker == nil && !f.DockerCLI:
		return Plan{Note: "Docker isn't installed, and sandboxes need it here: https://docs.docker.com/engine/install/ Run this again afterwards, for gVisor."}
	case f.Docker == nil:
		return Plan{Note: "can't reach Docker; is it running, and may you use it (e.g. are you in the docker group)? Run this again once `docker info` works, for gVisor."}
	}
	info := *f.Docker
	switch used := platform.Kind(info, f.used()); {
	case used == platform.Kata:
		return Plan{Best: true, Note: "each new sandbox already runs in a VM of its own (Kata Containers, " + whyRuntime(f) + ")"}
	case f.InVM:
		return Plan{Note: engineName(f) + " keeps all your containers in one Linux VM, and that's the most it can do: " + own + "needs Docker Engine."}
	case userns(f) == ", rootless" && used != platform.Gvisor:
		return Plan{Note: own + "isn't set up automatically with rootless Docker: see https://gvisor.dev/docs/user_guide/install/"}
	case f.Machine != "amd64" && f.Machine != "arm64" && platform.RuntimeOf(info, platform.Gvisor) == "":
		return Plan{Note: own + "has no build for " + f.Arch + "."}
	}

	// What Docker has, whatever is chosen now: a gVisor runtime, and one
	// that may connect to the ssh-agent socket a sandbox is given.
	have, haveUDS, runsc := false, false, f.RunscPath
	for name, rt := range info.Runtimes {
		if platform.Kind(info, name) != platform.Gvisor || rt.Missing {
			continue // one whose program is gone is installed again
		}
		have, haveUDS = true, haveUDS || platform.HostUDS(rt)
		if filepath.IsAbs(rt.Path) {
			runsc = rt.Path
		}
	}
	wantUDS := platform.AgentPossible(f.GOOS, info)

	p := Plan{What: "gVisor", Gain: "each new sandbox runs on a user-space kernel of its own instead of directly on this machine's, so a kernel bug is much harder to reach from inside it"}
	switch {
	case runsc != "":
	case have: // Docker finds it, so root's PATH has it
		runsc = "runsc"
	default:
		runsc = "/usr/local/bin/runsc"
		p.Steps = append(p.Steps,
			Step{Title: "Download gVisor's latest release from storage.googleapis.com (about 85 MB) and check it against the SHA-512 published with it", Fetch: FetchGvisor},
			Step{Title: "Install it in /usr/local/bin", Cmds: [][]string{
				{"sudo", "install", "-m", "755", Downloads + "/runsc", Downloads + "/containerd-shim-runsc-v1", "/usr/local/bin/"},
				{"sudo", "cp", "-R", Downloads + "/gvisor-bin", "/usr/local/bin/"},
			}})
	}
	plain := []string{"sudo", runsc, "install"}
	ssh := []string{"sudo", runsc, "install", "--runtime", "runsc-ssh", "--", "--host-uds=open"}
	const agent = "may also connect to the ssh-agent socket a sandbox is given, so that keys stay on this machine"
	switch {
	case !have && wantUDS:
		p.Steps = append(p.Steps, Step{Title: "Tell Docker about it: a runtime runsc, and runsc-ssh, which " + agent, Cmds: [][]string{plain, ssh}})
	case !have:
		p.Steps = append(p.Steps, Step{Title: "Tell Docker about it: a runtime runsc", Cmds: [][]string{plain}})
	case wantUDS && !haveUDS:
		p.What = "gVisor with your ssh-agent"
		p.Gain = "sandboxes on gVisor can use an SSH key through an ssh-agent on this machine, and can't read or copy it"
		p.Steps = append(p.Steps, Step{Title: "Tell Docker about a second gVisor runtime, runsc-ssh, which " + agent, Cmds: [][]string{ssh}})
	}
	if len(p.Steps) > 0 {
		reload := []string{"sudo", "pkill", "-HUP", "-x", "dockerd"}
		if f.Systemd {
			reload = []string{"sudo", "systemctl", "reload", "docker"}
		}
		p.Steps = append(p.Steps, Step{Title: "Have Docker read its configuration again (running containers carry on)", Cmds: [][]string{reload}})
	}

	// What the environment says wins over what Docker has.
	if f.RuntimeEnv != "" && platform.Kind(info, f.RuntimeEnv) != platform.Gvisor {
		p.After = fmt.Sprintf("%s=%s is set, which keeps new sandboxes off gVisor: unset it.", platform.RuntimeEnv, f.RuntimeEnv)
	} else if f.RuntimeEnv != "" && wantUDS && !platform.HostUDS(info.Runtimes[f.RuntimeEnv]) {
		p.After = fmt.Sprintf("%s=%s is set, a runtime that can't reach your ssh-agent: unset it, and new sandboxes use the one that can.", platform.RuntimeEnv, f.RuntimeEnv)
	}
	if len(p.Steps) > 0 {
		return p
	}
	if p.After != "" {
		return Plan{Note: "gVisor is set up, but " + p.After}
	}
	return Plan{Best: true, Note: "each new sandbox already runs on a kernel of its own (gVisor, " + whyRuntime(f) + ")"}
}
