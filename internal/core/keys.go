package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/sshagent"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// homeKeys lists the key pairs in ~/.ssh (private key paths).
func homeKeys() []string {
	pubs, _ := filepath.Glob(filepath.Join(state.Home(), ".ssh", "*.pub"))
	sort.Strings(pubs)
	var keys []string
	for _, pub := range pubs {
		if strings.HasPrefix(filepath.Base(pub), ".") { // like a shell glob
			continue
		}
		if priv := strings.TrimSuffix(pub, ".pub"); isFile(pub) && isFile(priv) {
			keys = append(keys, priv)
		}
	}
	return keys
}

var keyNameOK = regexp.MustCompile(`^[A-Za-z0-9._@+-]*$`)

// KeyChoice is an SSH key a new sandbox may get.
type KeyChoice struct {
	Value  string // what -k takes: a key's name in ~/.ssh, "new" or "none"
	Label  string
	Detail string
}

// KeyChoices lists the SSH keys a new sandbox may get here, and why keys
// from ~/.ssh aren't among them ("" when they are).
func (a *Core) KeyChoices() ([]KeyChoice, string) {
	var out []KeyChoice
	why := ""
	if a.plat.AgentOK {
		for _, k := range homeKeys() {
			out = append(out, KeyChoice{Value: filepath.Base(k), Label: filepath.Base(k), Detail: sshagent.Describe(a.r, k+".pub")})
		}
	} else {
		why = fmt.Sprintf("Keys from ~/.ssh aren't offered because %s: they would have to be copied into the sandbox. A new disposable key is stored inside it.", a.plat.NoAgentWhy)
	}
	detail := "for this sandbox only: kept on this machine, deleted with it"
	if !a.plat.AgentOK {
		detail = "for this sandbox only: stored inside it, deleted with it"
	}
	return append(out,
		KeyChoice{Value: "new", Label: "new disposable key", Detail: detail},
		KeyChoice{Value: "none", Label: "none", Detail: "no SSH access"}), why
}

// chooseKey sets the sandbox's key from want: a key in ~/.ssh, "new" or
// "none".
func (a *Core) chooseKey(sb *sandbox, want string) error {
	switch want {
	case "none":
		sb.keyKind, sb.keyPath, sb.keyName = "none", "none", ""
	case "new":
		sb.keyKind, sb.keyPath, sb.keyName = "disposable", state.DisposableKey(sb.name), "sandbox_key"
	default:
		if !a.plat.AgentOK {
			return fmt.Errorf("keys from ~/.ssh can't be used here: %s, so the key would have to be copied into the sandbox, which opencode-sandbox never does; use -k new (a disposable key) or -k none", a.plat.NoAgentWhy)
		}
		if !strings.Contains(want, "/") {
			want = state.Home() + "/.ssh/" + want
		}
		if !isFile(want) || !isFile(want+".pub") {
			return fmt.Errorf("no key pair at %s (+ .pub)", want)
		}
		real, err := state.ReadlinkF(want)
		if err != nil {
			return err
		}
		sb.keyKind, sb.keyPath, sb.keyName = "home", real, want[strings.LastIndexByte(want, '/')+1:]
	}
	switch {
	case sb.keyKind == "none":
		sb.keyMode = "none"
	case sb.keyKind == "home" || a.plat.AgentOK:
		sb.keyMode = "agent"
	default:
		sb.keyMode = "file"
	}
	if !keyNameOK.MatchString(sb.keyName) || strings.Contains(sb.keyPath, ",") {
		return fmt.Errorf("unsupported key path: %s", sb.keyPath)
	}
	return nil
}

func (a *Core) newDisposableKey(sb *sandbox) error {
	dir := filepath.Dir(sb.keyPath)
	if err := state.MkdirPrivate(state.KeysDir(), dir); err != nil {
		return err
	}
	if present(sb.keyPath) || present(sb.keyPath+".pub") {
		return fmt.Errorf("a disposable key '%s' already exists in %s (left from a deleted sandbox?); delete it first", sb.name, dir)
	}
	comment := fmt.Sprintf("opencode-sandbox-%s-%s", sb.name, time.Now().Format("2006-01-02"))
	var out bytes.Buffer
	err := run.Check(a.r, &run.Cmd{
		Args:   []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", sb.keyPath},
		Stdout: &out, Stderr: &out,
	})
	if err != nil {
		return fmt.Errorf("ssh-keygen couldn't make a key: %s", strings.TrimSpace(out.String()))
	}
	// Mounted into the sandbox as a file in file mode: readable there even
	// where Docker maps your files to another uid; the keys folder stays
	// user-only here.
	if sb.keyMode == "file" {
		return os.Chmod(sb.keyPath, 0o644)
	}
	return nil
}

// discardKey deletes a sandbox's disposable key pair. It touches nothing
// but a key that is plainly that sandbox's: at keys/NAME (the bash
// launcher's layout) or keys/NAME/id_ed25519, and made by opencode-sandbox
// for that sandbox.
func (a *Core) discardKey(name, key string) {
	key = filepath.Clean(key)
	dir := filepath.Dir(key)
	legacy := filepath.Base(key) == name && filepath.Base(dir) == "keys"
	current := filepath.Base(key) == "id_ed25519" && filepath.Base(dir) == name && filepath.Base(filepath.Dir(dir)) == "keys"
	if !legacy && !current {
		return
	}
	src := key + ".pub"
	if !isFile(src) {
		src = key
	}
	if !strings.HasPrefix(sshagent.Comment(a.r, src), "opencode-sandbox-"+name+"-") {
		return
	}
	_ = os.Remove(key)
	_ = os.Remove(key + ".pub")
	if current {
		_ = os.Remove(dir) // only if it's empty
	}
}

// NewKey is a new disposable key, for the user to authorize where the
// sandbox may log in.
type NewKey struct {
	Sandbox     string
	PublicKey   string
	Fingerprint string
	Dir         string // where it's kept on this machine
	// Inside: its private part is stored inside the sandbox too, because
	// (Why) no host ssh-agent can serve it there.
	Inside bool
	Why    string
}

func (a *Core) newKey(sb *sandbox) *NewKey {
	pub, _ := os.ReadFile(sb.keyPath + ".pub")
	k := &NewKey{Sandbox: sb.name, PublicKey: strings.TrimSpace(string(pub)), Dir: filepath.Dir(sb.keyPath),
		Fingerprint: a.quietFingerprint(sb.keyPath + ".pub")}
	if sb.keyMode == "file" {
		k.Inside, k.Why = true, a.plat.NoAgentWhy
	}
	return k
}

// checkPath refuses paths that would hand a sandbox your secrets, or let it
// write among them: used for -w and --cp (what goes in) and for --get (where
// files land). what is e.g. "copy" or "copy files into".
func (a *Core) checkPath(path, what string) error {
	home, err := state.ReadlinkF(state.Home())
	if err != nil {
		home = state.Home()
	}
	contains := func(outer, inner string) bool { // outer is inner or one of its parents
		return strings.HasPrefix(inner+"/", strings.TrimSuffix(outer, "/")+"/")
	}
	// Never your home dir, or anything containing it: ~/.ssh is in there.
	if contains(path, home) {
		return fmt.Errorf("refusing to %s %s: it contains your home directory", what, path)
	}
	for _, p := range []string{".ssh", ".gnupg", ".config", ".local", ".aws", ".kube", ".docker"} {
		if contains(home+"/"+p, path) {
			return fmt.Errorf("refusing to %s %s: it is inside ~/%s", what, path, p)
		}
	}
	// Nor where disposable keys are kept.
	for _, d := range a.keyDirs() {
		if contains(path, d) || contains(d, path) {
			return fmt.Errorf("refusing to %s %s: it is in or contains %s, which holds opencode-sandbox's disposable keys", what, path, d)
		}
	}
	return nil
}

// keyDirs are the folders that hold disposable keys: this version's data
// folder, a bash launcher's clone, and wherever existing sandboxes' keys are.
func (a *Core) keyDirs() []string {
	if a.keyDirsCache != nil {
		return a.keyDirsCache
	}
	dirs := []string{state.DataDir()}
	if legacy := os.Getenv("OPENCODE_SANDBOX_LEGACY_DIR"); legacy != "" {
		dirs = append(dirs, legacy)
	}
	if rows, err := a.rows(); err == nil {
		for _, r := range rows {
			if r.KeyKind == "disposable" && filepath.IsAbs(r.Key) {
				dirs = append(dirs, filepath.Dir(r.Key))
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if real, err := state.ReadlinkF(d); err == nil {
			d = real
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	a.keyDirsCache = out
	return out
}
