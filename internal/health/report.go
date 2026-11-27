package health

import (
	"fmt"
	"io"
	"strings"

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
	options(p, f)
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
	p.item("ssh keys", "only a disposable key or none, stored inside the sandbox, where anything running there can copy it: "+f.Agent.NoAgentWhy)
	p.item("api keys", "only the chosen providers' keys, which can be read inside the sandbox")
	p.item("network", "open: the internet, your local network and services on this Mac, but not other sandboxes (each has a network of its own)")
	p.item("hardening", "non-root user, read-only root filesystem, no capabilities; the VM has 4 GB of memory and 4 CPUs. Apple container has no no-new-privileges or process limit; the VM stands in for them.")
}

// isolation describes what a sandbox created now gets.
func isolation(p *printer, f Facts) {
	p.section("How a new sandbox is isolated")
	p.item("boundary", boundary(f))
	p.item("files", "a copy of your folder in /workspace, never the folder itself")
	if f.Agent.AgentOK {
		p.item("ssh keys", "a key from ~/.ssh or a disposable one stays on this machine: the sandbox uses it through its own ssh-agent and can't read or copy it")
	} else {
		p.item("ssh keys", "only a disposable key or none, stored inside the sandbox, where anything running there can copy it: "+f.Agent.NoAgentWhy)
	}
	p.item("api keys", "only the chosen providers' keys, which can be read inside the sandbox")
	p.item("network", "open: the internet, your local network and services on this machine, but not other sandboxes")

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
	h += fmt.Sprintf(", at most %d processes; no memory or CPU limit", f.PidsLimit)
	if !seccomp(f) {
		h += "; no seccomp filter, as Docker's is switched off"
	}
	p.item("hardening", h)
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
	if f.Runtime != "" {
		return "runtime " + f.Runtime + ", chosen with " + platform.RuntimeEnv
	}
	return "runtime " + f.Docker.DefaultRuntime + ", Docker's default"
}

// options lists the kernel boundaries this machine has or could have, the
// one in use first.
func options(p *printer, f Facts) {
	p.section("Isolation options here")
	if f.Docker != nil {
		used, how := "used", ""
		if f.Engine != "docker" || platform.Kind(*f.Docker, f.used()) != "" {
			used = "available"
		}
		if f.Engine != "docker" {
			how = "; set " + platform.EngineEnv + "=docker to use it"
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
		gvisor(p, f)
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

func appleContainer(p *printer, f Facts) {
	what := "Apple container: one lightweight VM for each sandbox, so sandboxes don't share a kernel with each other or with your other containers."
	switch {
	case !f.AppleSilicon:
		p.item("no", "Apple container needs a Mac with Apple silicon")
	case f.MacOS > 0 && f.MacOS < 26:
		p.item("no", fmt.Sprintf("Apple container needs macOS 26 or later (this is %s)", f.System))
	case !f.AppleCLI:
		p.item("install", what+" Get it from https://github.com/apple/container/releases and run "+cmd("container system start")+"; new sandboxes then use it.")
	case !f.AppleRunning:
		p.item("start", what+" "+f.AppleVersion+" is installed; run "+cmd("container system start")+", and new sandboxes use it.")
	case f.Engine == "apple":
		p.item("used", "Apple container "+f.AppleVersion+": a lightweight VM for each sandbox")
	default:
		p.item("available", what+" "+f.AppleVersion+" is running; unset "+platform.EngineEnv+" to use it.")
	}
}

func gvisor(p *printer, f Facts) {
	what := "gVisor: each sandbox runs on its own user-space kernel, so a kernel bug is much harder to reach from inside it."
	setup := "run " + cmd("sudo runsc install") + " and " + cmd("sudo systemctl reload docker") + "."
	// Without --host-uds=open, a host ssh-agent can't serve gVisor sandboxes.
	uds := func(rt string) string {
		if p := platform.DecideOn(f.GOOS, *f.Docker, rt); p.AgentOK || !strings.HasPrefix(p.NoAgentWhy, "gVisor") {
			return ""
		}
		return " Its sandboxes get SSH keys only stored inside them; for keys from ~/.ssh, add a runtime that may reach the host's ssh-agent with " + cmd("sudo runsc install --runtime runsc-ssh -- --host-uds=open") + " and use that one."
	}
	switch rt := runtimeOf(f.Docker, platform.Gvisor); {
	case f.Docker != nil && platform.Kind(*f.Docker, f.used()) == platform.Gvisor:
		p.item("used", "gVisor ("+whyRuntime(f)+"): each sandbox runs on its own user-space kernel."+uds(f.used()))
	case rt != "":
		p.item("available", what+" Docker has it as runtime "+rt+"; set "+platform.RuntimeEnv+"="+rt+" to use it."+uds(rt))
	case f.InVM:
		p.item("no", "gVisor needs Docker Engine; Docker Desktop's VM doesn't have it")
	case f.RunscOnPath:
		p.item("set up", what+" runsc is installed but Docker doesn't know it: "+setup+" Then set "+platform.RuntimeEnv+"=runsc to use it.")
	default:
		p.item("install", what+" Get it from https://gvisor.dev/docs/user_guide/install/, then "+setup+" Then set "+platform.RuntimeEnv+"=runsc to use it.")
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
