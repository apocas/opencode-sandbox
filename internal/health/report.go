package health

import (
	"fmt"
	"io"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/gateway"
	"github.com/apocas/opencode-sandbox/internal/platform"
)

// Doc is the health report: a title and sections of tagged items. OK is
// false if sandboxes can't be created here as things stand.
type Doc struct {
	Title    string
	Sections []Section
	OK       bool
}

// Section is a titled list of items.
type Section struct {
	Title string
	Items []Item
}

// Item is one line of the report: a tag ("ok", "no", "--", "used",
// "available", "install", "start", "set up", "trade-off", or a label such
// as "boundary") and its text. NBSP in the text marks spaces not to wrap at.
type Item struct{ Tag, Text string }

// Report writes the health report as plain text and returns Doc.OK.
func Report(w io.Writer, f Facts) bool {
	d := Build(f)
	Plain(w, d)
	return d.OK
}

// Build makes the health report from the facts.
func Build(f Facts) Doc {
	p := &printer{doc: &Doc{Title: fmt.Sprintf("opencode-sandbox %s on %s (%s)", f.Version, f.System, f.Arch)}}
	ok := setup(p, f)
	switch f.Engine {
	case "docker":
		isolation(p, f)
	case "apple":
		appleIsolation(p, f)
	}
	plan := PlanFor(f)
	options(p, f, plan)
	if len(plan.Steps) > 0 {
		p.section("Next step")
		p.item("run", cmd("opencode-sandbox setup")+" sets up "+plan.What+": "+plan.Gain+". It shows what it will do and asks before doing it.")
	}
	p.doc.OK = ok
	return *p.doc
}

// Plain writes the report as plain text, wrapped to 80 columns.
func Plain(w io.Writer, d Doc) {
	fmt.Fprintln(w, d.Title)
	for _, s := range d.Sections {
		fmt.Fprintf(w, "\n%s\n", s.Title)
		for _, it := range s.Items {
			tag := it.Tag
			for i, line := range wrap(it.Text, textWidth) {
				if i > 0 {
					tag = ""
				}
				fmt.Fprintf(w, "  %-*s %s\n", tagWidth, tag, strings.ReplaceAll(line, NBSP, " "))
			}
		}
	}
}

func setup(p *printer, f Facts) bool {
	p.section("Setup")
	ok := true
	fail := func(text string) { ok = false; p.item("no", text) }

	if f.Root {
		fail("you're root: run opencode-sandbox as yourself, since sandboxes run with your uid")
	}
	switch {
	case f.Docker != nil:
		p.item("ok", fmt.Sprintf("Docker %s (%s%s)", f.Docker.Version, f.Docker.OS, userns(f)))
	case f.Engine == "apple" && f.DockerCLI:
		p.item("--", "can't reach Docker; only sandboxes made with it need it")
	case f.Engine == "apple":
	case !f.DockerCLI && f.GOOS == "linux":
		fail("Docker isn't installed: https://docs.docker.com/engine/install/")
	case !f.DockerCLI:
		fail("Docker isn't installed: https://docs.docker.com/get-started/get-docker/")
	default:
		fail("can't reach Docker; is it running, and may you use it (e.g. are you in the docker group)?")
	}
	switch {
	case f.AppleRunning:
		p.item("ok", "Apple container "+f.AppleVersion)
	case f.AppleCLI:
		p.item("--", "Apple container "+f.AppleVersion+" is installed but not running; start it with "+cmd("container system start"))
	}
	if strings.HasPrefix(f.EngineErr, platform.EngineEnv) {
		fail(f.EngineErr)
	}
	if f.RuntimeErr != "" {
		fail(f.RuntimeErr)
	}
	if f.LimitsErr != "" {
		fail(f.LimitsErr)
	}

	cfg := f.tilde(f.Config)
	switch f.ConfigErr {
	case "":
		p.item("ok", fmt.Sprintf("opencode config %s, %s", cfg, plural(f.Providers, "provider")))
	case "missing":
		fail("no opencode config at " + cfg + "; new sandboxes take their providers from it (set OPENCODE_SANDBOX_CONFIG to use another)")
	case "not JSON":
		fail("can't parse " + cfg + " as JSON")
	default:
		fail("no providers are configured in " + cfg)
	}

	if len(f.MissingSSH) == 0 {
		p.item("ok", "OpenSSH: ssh-keygen, ssh-agent, ssh-add")
	} else {
		p.item("--", "missing "+strings.Join(f.MissingSSH, ", ")+": sandboxes with an SSH key need OpenSSH's tools; -k none works without them")
	}

	if f.Engine != "" {
		in := map[string]string{"docker": "Docker", "apple": "Apple container"}[f.Engine]
		if f.ImageOK {
			p.item("ok", "image "+f.Image+" in "+in)
		} else {
			p.item("--", "image "+f.Image+" isn't built in "+in+" yet; the first new sandbox builds it (a few minutes)")
		}
	}
	return ok
}

// appleIsolation describes what a sandbox created now in Apple container
// gets.
func appleIsolation(p *printer, f Facts) {
	p.section("How a new sandbox is isolated")
	p.item("boundary", "a lightweight VM of its own (Apple container). A sandbox that breaks out of its container reaches only its own VM, which sees no more of this Mac than the folders mounted into it: its config and its disposable key.")
	p.item("files", "a copy of your folder in /workspace, never the folder itself")
	p.item("ssh keys", sshKeys(f, "only a disposable key or none, stored inside the sandbox, where anything running there can copy it: "+f.Agent.NoAgentWhy))
	p.item("api keys", apiKeys(f))
	p.item("network", network(f, "open: the internet, your local network and services on this Mac, but not other sandboxes (each has a network of its own)"))
	p.item("hardening", "non-root user, read-only root filesystem, no capabilities; the VM has "+limits(f)+". Apple container has no no-new-privileges or process limit; the VM stands in for them.")
}

// isolation describes what a sandbox created now gets.
func isolation(p *printer, f Facts) {
	p.section("How a new sandbox is isolated")
	p.item("boundary", boundary(f))
	p.item("files", "a copy of your folder in /workspace, never the folder itself")
	if f.Agent.AgentOK {
		p.item("ssh keys", sshKeys(f, "a key from ~/.ssh or a disposable one stays on this machine: the sandbox uses it through its own ssh-agent and can't read or copy it"))
	} else {
		p.item("ssh keys", sshKeys(f, "only a disposable key or none, stored inside the sandbox, where anything running there can copy it: "+f.Agent.NoAgentWhy))
	}
	p.item("api keys", apiKeys(f))
	p.item("network", network(f, "open: the internet, your local network and services on this machine, but not other sandboxes"))

	h := "non-root user, read-only root filesystem, no capabilities, no-new-privileges"
	for _, o := range f.Docker.SecurityOptions {
		switch {
		case strings.HasPrefix(o, "name=apparmor"):
			h += ", AppArmor"
		case strings.HasPrefix(o, "name=selinux"):
			h += ", SELinux"
		}
	}
	if seccomp(f) {
		h += ", seccomp"
	}
	h += fmt.Sprintf("; at most %d processes, %s", f.PidsLimit, limits(f))
	if !seccomp(f) {
		h += "; no seccomp filter, as Docker's is switched off"
	}
	p.item("hardening", h)
}

// limits says the memory and CPUs a new sandbox may use.
func limits(f Facts) string {
	switch {
	case f.Limits == "":
		return "no memory or CPU limit"
	case strings.Contains(f.Limits, "no "):
		return f.Limits + " (Docker here can't hold a container to one)"
	}
	return f.Limits + " (set others with `opencode-sandbox new --memory 8g --cpus 6`)"
}

// sshKeys says which SSH keys a new sandbox may get: none without a network.
func sshKeys(f Facts, with string) string {
	if f.NoNetwork {
		return "none: without a network, a sandbox has nowhere to log in to"
	}
	return with
}

// network says what a new sandbox can reach: open, unless new sandboxes get
// no network.
func network(f Facts, open string) string {
	if f.NoNetwork {
		return "none (OPENCODE_SANDBOX_NETWORK): a sandbox reaches only its providers, through the gateway. No SSH, no downloads; `opencode-sandbox new --network open` makes one that can."
	}
	return open + ". `opencode-sandbox new --network none` makes one that reaches only its providers, through the gateway."
}

// apiKeys says where the API keys of a new sandbox's providers are.
func apiKeys(f Facts) string {
	if !f.Gateway {
		return "only the chosen providers' keys, which can be read inside the sandbox (" + gateway.Switch + " is off)"
	}
	s := "stay on this machine: a sandbox reaches its providers through a gateway here that puts the key into each request, so it can use them while it's open and can't read or copy their keys."
	for _, k := range f.KeysInside {
		s += " Except a secret of " + k.Provider + ", which goes into the sandbox: " + k.Why + "."
	}
	return s
}

func boundary(f Facts) string {
	switch platform.Kind(*f.Docker, f.used()) {
	case platform.Gvisor:
		return "gVisor (" + whyRuntime(f) + "): each sandbox runs on its own user-space kernel rather than directly on this machine's, so a kernel bug is much harder to reach from inside it."
	case platform.Kata:
		return "Kata Containers (" + whyRuntime(f) + "): each sandbox runs in its own VM."
	}
	if f.InVM {
		return vmName(f) + ", shared by all your containers. A sandbox that breaks out of its container reaches that VM, your other containers and the folders shared into the VM from this machine (usually including your home folder)."
	}
	b := "this machine's kernel, shared with your other containers. Namespaces separate a sandbox, but one that exploits a kernel bug reaches this machine"
	if seccomp(f) {
		b = strings.Replace(b, "Namespaces separate", "Namespaces and seccomp separate", 1)
	}
	switch u := userns(f); u {
	case ", rootless":
		b += " with your user's rights (Docker is rootless), not root's"
	case ", userns-remap":
		b += " as an unprivileged user (Docker uses userns-remap), unless the bug also gives root"
	}
	return b + "."
}

// whyRuntime says how the runtime in use was chosen.
func whyRuntime(f Facts) string {
	switch {
	case f.RuntimeAuto:
		return "runtime " + f.Runtime + ", used because Docker has it"
	case f.Runtime != "":
		return "runtime " + f.Runtime + ", chosen with " + platform.RuntimeEnv
	}
	return "runtime " + f.Docker.DefaultRuntime + ", Docker's default"
}

// options lists the kernel boundaries this machine has or could have, the
// one in use first. Where plan has steps, `opencode-sandbox setup` does what
// an option needs.
func options(p *printer, f Facts, plan Plan) {
	p.section("Isolation options here")
	if f.Docker != nil {
		used, how := "used", ""
		if f.Engine != "docker" || platform.Kind(*f.Docker, f.used()) != "" {
			used = "available"
		}
		switch {
		case f.Engine != "docker":
			how = "; set " + platform.EngineEnv + "=docker to use it"
		case f.RuntimeAuto:
			how = "; set " + platform.RuntimeEnv + "=" + f.Docker.DefaultRuntime + " to use it"
		}
		switch {
		case f.InVM:
			p.item(used, engineName(f)+": one Linux VM for all your containers"+how)
		default:
			p.item(used, "Docker Engine: sandboxes share this machine's kernel"+how)
		}
	}
	switch f.GOOS {
	case "darwin":
		appleContainer(p, f)
	case "linux":
		gvisor(p, f, plan)
		if f.Docker != nil && platform.Kind(*f.Docker, f.used()) == platform.Kata {
			p.item("used", "Kata Containers ("+whyRuntime(f)+"): a VM for each sandbox")
		} else if rt := runtimeOf(f.Docker, platform.Kata); rt != "" {
			p.item("available", "Kata Containers: a VM for each sandbox. Docker has it as runtime "+rt+"; set "+platform.RuntimeEnv+"="+rt+" to use it.")
		}
		if f.InVM {
			p.item("trade-off", "Docker Engine instead of Docker Desktop would keep SSH keys out of sandboxes (a host ssh-agent serves them), but sandboxes would share this machine's kernel unless you add gVisor: https://docs.docker.com/engine/install/")
		}
	}
}

// setupCmd is the command that does what an option needs.
var setupCmd = cmd("opencode-sandbox setup")

func appleContainer(p *printer, f Facts) {
	what := "Apple container: one lightweight VM for each sandbox, so sandboxes don't share a kernel with each other or with your other containers."
	switch {
	case !f.AppleSilicon:
		p.item("no", "Apple container needs a Mac with Apple silicon")
	case f.MacOS > 0 && f.MacOS < 26:
		p.item("no", fmt.Sprintf("Apple container needs macOS 26 or later (this is %s)", f.System))
	case !f.AppleCLI:
		p.item("install", what+" "+setupCmd+" installs it (from https://github.com/apple/container/releases); new sandboxes then use it.")
	case !f.AppleRunning:
		p.item("start", what+" "+f.AppleVersion+" is installed; "+setupCmd+" (or "+cmd("container system start")+") starts it, and new sandboxes use it.")
	case f.Engine == "apple":
		p.item("used", "Apple container "+f.AppleVersion+": a lightweight VM for each sandbox")
	default:
		p.item("available", what+" "+f.AppleVersion+" is running; unset "+platform.EngineEnv+" to use it.")
	}
}

func gvisor(p *printer, f Facts, plan Plan) {
	what := "gVisor: each sandbox runs on its own user-space kernel, so a kernel bug is much harder to reach from inside it."
	const byHand = "https://gvisor.dev/docs/user_guide/install/"
	auto := len(plan.Steps) > 0
	// Without --host-uds=open, a host ssh-agent can't serve gVisor sandboxes.
	inside := func(rt string) bool {
		d := platform.DecideOn(f.GOOS, *f.Docker, rt)
		return !d.AgentOK && strings.HasPrefix(d.NoAgentWhy, "gVisor")
	}
	switch rt := runtimeOf(f.Docker, platform.Gvisor); {
	case f.Docker != nil && platform.Kind(*f.Docker, f.used()) == platform.Gvisor:
		text := "gVisor (" + whyRuntime(f) + "): each sandbox runs on its own user-space kernel."
		switch {
		case !inside(f.used()):
		case auto:
			text += " Its sandboxes get SSH keys only stored inside them; " + setupCmd + " adds a runtime that may reach the host's ssh-agent, for keys from ~/.ssh."
		default:
			text += " Its sandboxes get SSH keys only stored inside them; unset " + platform.RuntimeEnv + " for a runtime that may reach the host's ssh-agent, for keys from ~/.ssh."
		}
		p.item("used", text)
	case rt != "" && auto:
		p.item("set up", what+" Docker has it as runtime "+rt+", which can't reach the host's ssh-agent: its sandboxes couldn't use keys from ~/.ssh, so it isn't used unless you ask. "+
			setupCmd+" adds a runtime that can, and new sandboxes then use it. To use "+rt+" as it is, set "+platform.RuntimeEnv+"="+rt+".")
	case rt != "":
		p.item("available", what+" Docker has it as runtime "+rt+"; unset "+platform.RuntimeEnv+" to use it.")
	case f.InVM:
		p.item("no", "gVisor needs Docker Engine; Docker Desktop's VM doesn't have it")
	case f.Docker == nil:
		p.item("install", what+" Once Docker works, "+setupCmd+" installs it.")
	case platform.Kind(*f.Docker, f.used()) == platform.Kata: // a VM each: gVisor would add nothing
	case userns(f) == ", rootless":
		p.item("install", what+" With rootless Docker, it isn't set up for you: "+byHand)
	case !auto:
		p.item("no", "gVisor has no build for "+f.Arch)
	case f.RunscPath != "":
		p.item("set up", what+" runsc is installed but Docker doesn't know it: "+setupCmd+" registers it (by hand: "+byHand+"), and new sandboxes then use it.")
	default:
		p.item("install", what+" "+setupCmd+" installs it (by hand: "+byHand+"), and new sandboxes then use it.")
	}
}

func engineName(f Facts) string {
	if strings.Contains(f.Docker.OS, "Docker Desktop") {
		return "Docker Desktop"
	}
	return "Docker (" + f.Docker.OS + ")"
}

func vmName(f Facts) string {
	if strings.Contains(f.Docker.OS, "Docker Desktop") {
		return "Docker Desktop's Linux VM"
	}
	return "Docker's Linux VM (" + f.Docker.OS + ")"
}

// seccomp is true if Docker filters containers' system calls.
func seccomp(f Facts) bool {
	for _, o := range f.Docker.SecurityOptions {
		if strings.HasPrefix(o, "name=seccomp") && !strings.Contains(o, "profile=unconfined") {
			return true
		}
	}
	return false
}

func userns(f Facts) string {
	if f.Docker == nil {
		return ""
	}
	for _, o := range f.Docker.SecurityOptions {
		switch {
		case strings.HasPrefix(o, "name=rootless"):
			return ", rootless"
		case strings.HasPrefix(o, "name=userns"):
			return ", userns-remap"
		}
	}
	return ""
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// tilde shortens a path in your home folder to ~/…, so a pasted report
// doesn't show your user name.
func (f Facts) tilde(path string) string {
	if f.Home != "" && f.Home != "/" && strings.HasPrefix(path, f.Home+"/") {
		return "~" + path[len(f.Home):]
	}
	return path
}

// printer collects sections of tagged items.
type printer struct{ doc *Doc }

const (
	tagWidth  = 10
	textWidth = 79 - 2 - tagWidth - 1
)

func (p *printer) section(title string) {
	p.doc.Sections = append(p.doc.Sections, Section{Title: title})
}

func (p *printer) item(tag, text string) {
	s := &p.doc.Sections[len(p.doc.Sections)-1]
	s.Items = append(s.Items, Item{Tag: tag, Text: text})
}

// NBSP marks a space not to wrap at, in commands to type; renderers show it
// as a space.
const NBSP = "\x00"

// cmd quotes a command for the reader to type, kept on one line.
func cmd(s string) string { return "`" + strings.ReplaceAll(s, " ", NBSP) + "`" }

// wrap breaks text into lines of at most width columns at spaces; a longer
// word (a URL) gets a line of its own.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Split(text, " ") {
		switch {
		case word == "":
			continue
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}
