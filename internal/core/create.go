package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/occonfig"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// sandbox is a sandbox being created.
type sandbox struct {
	name, workspace string
	// keyKind is home, disposable or none; keyPath the private key (or
	// "none"); keyName its file name inside the sandbox; keyMode agent
	// (host ssh-agent), file (disposable key stored inside, only where no
	// agent is possible) or none.
	keyKind, keyPath, keyName, keyMode string
	providers                          string // comma-separated
	keyCreated, created                bool
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// CreateOptions describe a new sandbox. The interface decides them; see
// KeyChoices and ProviderChoices for what there is to choose from.
type CreateOptions struct {
	Workspace string   // folder to copy in ("" for the current folder)
	Name      string   // "" for the folder's name plus a random suffix
	Key       string   // a key pair in ~/.ssh (name or path), "new" or "none"
	Providers []string // provider ids; none for the only one configured
	Args      []string // passed to opencode
}

// Created is what Create made.
type Created struct {
	Name string
	// NewKey is a new disposable key to show the user, or nil.
	NewKey *NewKey
}

// Create makes a new sandbox without opening it. If it fails, nothing of it
// is left behind.
func (a *Core) Create(o CreateOptions) (*Created, error) {
	if err := a.CanCreate(); err != nil {
		return nil, err
	}
	e := a.newE
	workspace, err := a.ResolveWorkspace(o.Workspace)
	if err != nil {
		return nil, err
	}
	name := o.Name
	if name == "" {
		name = a.DefaultName(workspace)
	}
	if err := a.CheckName(name); err != nil {
		return nil, err
	}
	sb := &sandbox{name: name, workspace: workspace}
	if o.Key == "" {
		return nil, errors.New("no SSH key chosen; pass -k NAME, -k new or -k none")
	}
	if err := a.chooseKey(sb, o.Key); err != nil {
		return nil, err
	}
	if err := a.chooseProviders(sb, strings.Join(o.Providers, ",")); err != nil {
		return nil, err
	}
	defer a.Rep.Done()
	if err := a.prepare(e); err != nil {
		return nil, err
	}
	// If creating the sandbox fails, leave nothing of it behind.
	defer func() {
		if !sb.created {
			a.abandon(sb)
		}
	}()
	a.Rep.Step(fmt.Sprintf("Creating sandbox '%s'", name))
	if err := a.writeConfig(e, name, sb.providers); err != nil {
		return nil, err
	}
	if sb.keyKind == "disposable" {
		if err := a.newDisposableKey(sb); err != nil {
			return nil, err
		}
		sb.keyCreated = true
	}
	if sb.keyMode == "agent" {
		if err := a.agents.Ensure(name, sb.keyPath); err != nil {
			return nil, err
		}
	}
	if err := a.createContainer(e, sb, o.Args); err != nil {
		return nil, err
	}
	sb.created = true
	c := &Created{Name: name}
	if sb.keyKind == "disposable" {
		c.NewKey = a.newKey(sb)
	}
	return c, nil
}

// CheckName refuses a name that isn't valid or is taken.
func (a *Core) CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid sandbox name: %s (letters, digits, _ . and -, starting with a letter or digit)", name)
	}
	if a.engineOf(name) != nil {
		return fmt.Errorf("sandbox '%s' already exists; open it with: opencode-sandbox open %s", name, name)
	}
	return nil
}

// ResolveWorkspace checks the folder to copy into a new sandbox ("" for the
// current folder) and returns its real path.
func (a *Core) ResolveWorkspace(arg string) (string, error) {
	p := arg
	if p == "" {
		p = os.Getenv("PWD")
		if p == "" {
			p, _ = os.Getwd()
		}
	}
	dir, err := state.ReadlinkF(p)
	if err != nil {
		return "", fmt.Errorf("bad directory: %s", p)
	}
	if !isDir(dir) {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	if err := a.checkPath(dir, "copy"); err != nil {
		return "", err
	}
	return dir, nil
}

// DefaultName is the folder's name plus a random suffix.
func (a *Core) DefaultName(workspace string) string {
	base := workspace[strings.LastIndexByte(workspace, '/')+1:]
	base = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, base)
	if !regexp.MustCompile(`^[A-Za-z0-9]`).MatchString(base) {
		base = "sb" + base
	}
	for {
		b := make([]byte, 2)
		_, _ = rand.Read(b)
		name := base + "-" + hex.EncodeToString(b)
		if a.engineOf(name) == nil {
			return name
		}
	}
}

// ProviderChoices lists the providers in your opencode config.
func (a *Core) ProviderChoices() ([]occonfig.Provider, error) {
	root, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	return occonfig.Menu(root), nil
}

func (a *Core) loadConfig() (any, error) {
	cfg := state.Config()
	if !readable(cfg) {
		return nil, fmt.Errorf("missing %s", cfg)
	}
	root, err := occonfig.Load(cfg)
	if err != nil || root == nil || root == false {
		return nil, fmt.Errorf("cannot parse %s as JSON", cfg)
	}
	if len(occonfig.ProviderIDs(root)) == 0 {
		return nil, fmt.Errorf("no providers are configured in %s", cfg)
	}
	return root, nil
}

// chooseProviders sets sb.providers from want (comma-separated); with
// nothing chosen, the only provider configured.
func (a *Core) chooseProviders(sb *sandbox, want string) error {
	root, err := a.loadConfig()
	if err != nil {
		return err
	}
	ids := occonfig.ProviderIDs(root)
	if want == "" {
		if len(ids) > 1 {
			return fmt.Errorf("no provider chosen; pass -p NAME (have: %s)", strings.Join(ids, ","))
		}
		want = ids[0]
		a.note("using the only provider in your opencode config: %s", want)
	}
	for _, p := range splitList(want) {
		if !oneOf(p, ids...) {
			return fmt.Errorf("no provider '%s' in %s (have: %s)", p, state.Config(), strings.Join(ids, ","))
		}
	}
	sb.providers = want
	return nil
}

// tsv escapes a menu field as jq's @tsv did.
func tsv(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`).Replace(s)
}

// writeConfig (re)writes a sandbox's private opencode config: a filtered copy
// of yours. It lives in the runtime dir (RAM, user-only) and is regenerated
// on every start, so changes to your config (e.g. a rotated API key) reach
// the sandbox.
func (a *Core) writeConfig(e backend.Engine, name, providers string) error {
	cfg := state.Config()
	root, _ := occonfig.Load(cfg)
	ids := occonfig.ProviderIDs(root)
	var have []string
	for _, p := range splitList(providers) {
		if oneOf(p, ids...) {
			have = append(have, p)
		} else {
			a.note("provider '%s' is no longer in %s; sandbox '%s' won't get it", p, cfg, name)
		}
	}
	if len(have) == 0 {
		return fmt.Errorf("none of sandbox '%s''s providers (%s) are in %s any more", name, providers, cfg)
	}
	filtered, err := occonfig.Filter(root, have)
	if err != nil {
		return fmt.Errorf("could not filter %s", cfg)
	}
	if err := state.EnsureRunDir(); err != nil {
		return err
	}
	if !e.Caps().FileBinds {
		return writeSandboxDir(name, occonfig.Encode(filtered))
	}
	if err := state.MkdirPrivate(state.RunDir() + "/configs"); err != nil {
		return err
	}
	out := state.ConfigFile(name)
	if err := os.WriteFile(out+".tmp", occonfig.Encode(filtered), 0o600); err != nil {
		return fmt.Errorf("could not filter %s", cfg)
	}
	// Readable in the container even where Docker maps your files to another
	// uid (rootless); on this machine the user-only directory keeps it private.
	if err := os.Chmod(out+".tmp", 0o644); err != nil {
		return err
	}
	return os.Rename(out+".tmp", out)
}

// prepare is everything a sandbox needs on the host before it is created or
// resumed.
func (a *Core) prepare(e backend.Engine) error {
	if cfg := state.Config(); !readable(cfg) {
		return fmt.Errorf("missing %s", cfg)
	}
	if err := a.seedKnownHosts(); err != nil {
		return err
	}
	if !e.ImageExists(image()) {
		a.Rep.Step(fmt.Sprintf("Building the sandbox image %s (the first time takes a few minutes)", image()))
		if err := a.build(e, nil); err != nil {
			return fmt.Errorf("could not build the image: %w", err)
		}
	}
	// Sandboxes share this network but cannot talk to each other (icc off).
	if err := e.EnsureNetwork(state.Network); err != nil {
		return err
	}
	a.agents.GC()
	return nil
}

// seedKnownHosts creates the known_hosts every sandbox trusts: the keys of
// OPENCODE_SANDBOX_SEED_HOSTS from your ~/.ssh/known_hosts, so the first hop
// (e.g. a jump host) is verified. It's read once; delete the file to re-seed.
// A clone of the bash launcher kept its own copy, which is carried over.
func (a *Core) seedKnownHosts() error {
	out := state.KnownHosts()
	if isFile(out) {
		return nil
	}
	if err := state.MkdirPrivate(state.DataDir()); err != nil {
		return err
	}
	var b bytes.Buffer
	if legacy := os.Getenv("OPENCODE_SANDBOX_LEGACY_DIR"); legacy != "" && isFile(legacy+"/known_hosts") {
		data, err := os.ReadFile(legacy + "/known_hosts")
		if err != nil {
			return err
		}
		b.Write(data)
	} else {
		for _, h := range strings.Fields(os.Getenv("OPENCODE_SANDBOX_SEED_HOSTS")) {
			var found bytes.Buffer
			_, _ = a.r.Run(&run.Cmd{Args: []string{"ssh-keygen", "-F", h, "-f", state.Home() + "/.ssh/known_hosts"}, Stdout: &found})
			for _, line := range strings.SplitAfter(found.String(), "\n") {
				if line != "" && !strings.HasPrefix(line, "#") {
					b.WriteString(line)
				}
			}
		}
	}
	if err := os.WriteFile(out, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Chmod(out, 0o644)
}

// createContainer creates the sandbox's container in engine e and copies the
// workspace into it.
func (a *Core) createContainer(e backend.Engine, sb *sandbox, opencodeArgs []string) error {
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	s := backend.Spec{
		Container: state.Prefix + sb.name,
		TTY:       a.TTY,
		Labels: [][2]string{
			{state.LabelName, sb.name},
			{state.Label("source"), sb.workspace},
			{state.Label("keykind"), sb.keyKind},
			{state.Label("key"), sb.keyPath},
			{state.Label("keymode"), sb.keyMode},
			{state.Label("providers"), sb.providers},
			{state.Label("version"), Version},
			{state.Label("runtime"), a.runtime()},
		},
		// Hardening: immutable rootfs, no capabilities, no privilege
		// escalation, and only the variables below. Your host SSH_AUTH_SOCK
		// is NOT passed through. /tmp needs exec: opencode's TUI unpacks a
		// native .so there and loads it.
		Init: true, ReadOnlyRoot: true, DropAllCaps: true, NoNewPrivileges: true, PidsLimit: pidsLimit,
		Tmpfs:   []string{"/tmp:exec,size=1g,mode=1777"},
		Network: state.Network,
		Runtime: a.plat.Runtime,
		Env:     [][2]string{{"TERM", term}, {"COLORTERM", os.Getenv("COLORTERM")}},
		Volumes: []string{"/home/agent", "/workspace"},
		Binds: []backend.Bind{
			{Src: state.ConfigFile(sb.name), Dst: "/home/agent/.config/opencode/opencode.json", ReadOnly: true},
			{Src: state.KnownHosts(), Dst: "/etc/ssh/ssh_known_hosts", ReadOnly: true},
		},
		Image: image(),
		Cmd:   append([]string{"opencode"}, opencodeArgs...),
	}
	if !e.Caps().FileBinds {
		// Only folders can be mounted: the sandbox's own folder instead.
		s.Binds = []backend.Bind{{Src: state.SandboxDir(sb.name), Dst: "/run/opencode-sandbox/config", ReadOnly: true}}
		s.Env = append(s.Env, [2]string{"OPENCODE_CONFIG", "/run/opencode-sandbox/config/opencode.json"})
	}
	switch {
	case sb.keyMode == "file" && !e.Caps().FileBinds:
		// The key's folder holds only this key pair (see DisposableKey).
		if sb.keyPath != state.DisposableKey(sb.name) {
			return fmt.Errorf("can't mount %s into the sandbox on its own", sb.keyPath)
		}
		s.Env = append(s.Env, [2]string{"SSH_AUTH_SOCK", "/tmp/ssh-agent.sock"})
		s.Binds = append(s.Binds, backend.Bind{Src: filepath.Dir(sb.keyPath), Dst: "/run/opencode-sandbox/keydir", ReadOnly: true})
	case sb.keyMode == "agent":
		s.Env = append(s.Env, [2]string{"SSH_AUTH_SOCK", "/run/ssh-agent/ssh-agent.sock"})
		s.Binds = append(s.Binds,
			backend.Bind{Src: state.AgentDir(sb.name) + "/sock", Dst: "/run/ssh-agent", ReadOnly: true},
			backend.Bind{Src: sb.keyPath + ".pub", Dst: "/home/agent/.ssh/" + sb.keyName, ReadOnly: true},
			backend.Bind{Src: sb.keyPath + ".pub", Dst: "/home/agent/.ssh/" + sb.keyName + ".pub", ReadOnly: true})
	case sb.keyMode == "file":
		// No host agent possible: the disposable key goes in as a file, and
		// entrypoint.sh loads it into an agent inside the sandbox (for ssh -A).
		s.Env = append(s.Env, [2]string{"SSH_AUTH_SOCK", "/tmp/ssh-agent.sock"})
		s.Binds = append(s.Binds,
			backend.Bind{Src: sb.keyPath, Dst: "/run/opencode-sandbox/key", ReadOnly: true},
			backend.Bind{Src: sb.keyPath + ".pub", Dst: "/run/opencode-sandbox/key.pub", ReadOnly: true})
	}
	if err := e.Create(s); err != nil {
		return err
	}
	// Copy (never mount) the workspace in, keeping your uid so the agent can
	// edit it.
	if err := e.CopyIn(s.Container, sb.workspace+"/.", "/workspace", true); err != nil {
		_ = e.Remove(s.Container)
		return fmt.Errorf("could not copy %s into the sandbox", sb.workspace)
	}
	return nil
}

// writeSandboxDir fills a sandbox's own folder (see state.SandboxDir) with
// its config and a copy of the host keys it trusts.
func writeSandboxDir(name string, config []byte) error {
	dir := state.SandboxDir(name)
	if err := state.MkdirPrivate(state.RunDir()+"/sandboxes", dir); err != nil {
		return err
	}
	hosts, err := os.ReadFile(state.KnownHosts())
	if err != nil {
		return err
	}
	for file, data := range map[string][]byte{"opencode.json": config, "known_hosts": hosts} {
		out := filepath.Join(dir, file)
		if err := os.WriteFile(out+".tmp", data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(out+".tmp", out); err != nil {
			return err
		}
	}
	return nil
}

// runtime is the OCI runtime new sandboxes run in; "" outside Docker.
func (a *Core) runtime() string {
	switch {
	case a.newE == nil || a.newE.Name() != "docker":
		return ""
	case a.plat.Runtime != "":
		return a.plat.Runtime
	}
	return a.dockerInfo.DefaultRuntime
}

// abandon undoes a half-finished creation.
func (a *Core) abandon(sb *sandbox) {
	_ = os.Remove(state.ConfigFile(sb.name))
	_ = os.RemoveAll(state.SandboxDir(sb.name))
	sshagent.Stop(state.AgentDir(sb.name))
	if sb.keyCreated { // only a key we just made
		a.discardKey(sb.name, sb.keyPath)
	}
}
