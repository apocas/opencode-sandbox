package app

import (
	"errors"
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
	"github.com/apocas/opencode-sandbox/internal/tty"
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

// chooseKey sets the sandbox's key from want (a key in ~/.ssh, "new" or
// "none"), asking when it's empty.
func (a *app) chooseKey(sb *sandbox, want string) error {
	var keys []string
	if a.plat.AgentOK {
		keys = homeKeys()
	}
	total := len(keys) + 2
	if want == "" {
		if !tty.StdinIsTerminal() {
			return errors.New("no terminal to ask which SSH key to use; pass -k NAME, -k new or -k none")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Which SSH key may sandbox '%s' use?\n", sb.name)
		if !a.plat.AgentOK {
			why := fmt.Sprintf("(Keys from ~/.ssh aren't offered because %s: they would have to be copied into the sandbox. A new disposable key is stored inside it.)", a.plat.NoAgentWhy)
			for _, line := range strings.Split(tty.Fold(why, 76), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		for i, k := range keys {
			fmt.Fprintf(&b, "  %d) %-16s %s\n", i+1, filepath.Base(k), sshagent.Describe(a.r, k+".pub"))
		}
		fmt.Fprintf(&b, "  %d) %-16s %s\n", total-1, "new disposable", "a fresh key for this sandbox only, deleted with it")
		fmt.Fprintf(&b, "  %d) %-16s %s\n", total, "none", "no SSH access")
		os.Stderr.WriteString(b.String())
		var n int
		for {
			line, ok := tty.ReadLine(fmt.Sprintf("Choice [1-%d]: ", total))
			if !ok {
				return errors.New("no choice made")
			}
			var valid bool
			if n, valid = choice(line, total); valid {
				break
			}
		}
		switch {
		case n <= len(keys):
			want = keys[n-1]
		case n == total-1:
			want = "new"
		default:
			want = "none"
		}
	}

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

func (a *app) newDisposableKey(sb *sandbox) error {
	dir := filepath.Dir(sb.keyPath)
	if err := state.MkdirPrivate(state.KeysDir(), dir); err != nil {
		return err
	}
	if present(sb.keyPath) || present(sb.keyPath+".pub") {
		return fmt.Errorf("a disposable key '%s' already exists in %s (left from a deleted sandbox?); delete it first", sb.name, dir)
	}
	comment := fmt.Sprintf("opencode-sandbox-%s-%s", sb.name, time.Now().Format("2006-01-02"))
	err := run.Check(a.r, &run.Cmd{
		Args:   []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", sb.keyPath},
		Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if err != nil {
		return err
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
func (a *app) discardKey(name, key string) {
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

func (a *app) showNewKey(sb *sandbox) {
	var b strings.Builder
	b.WriteString("\n")
	dir := filepath.Dir(sb.keyPath)
	if sb.keyMode == "file" {
		b.WriteString(tty.Fold(fmt.Sprintf("Sandbox '%s' has a new disposable SSH key. Its private part is stored INSIDE the sandbox, because %s: anything running there can read and copy it. Authorize it only where that's acceptable, and remove it from authorized_keys when you delete the sandbox. A copy is kept in %s; both are deleted with the sandbox.", sb.name, a.plat.NoAgentWhy, dir), 78))
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "Sandbox '%s' has a new disposable SSH key. The private part stays on this\n", sb.name)
		fmt.Fprintf(&b, "machine in %s (never inside the sandbox) and is deleted with the sandbox.\n", dir)
	}
	b.WriteString("Authorize it where the sandbox may log in, e.g. in ~/.ssh/authorized_keys on the server:\n\n")
	pub, _ := os.ReadFile(sb.keyPath + ".pub")
	b.Write(pub)
	fmt.Fprintf(&b, "\nPrint it again any time with: opencode-sandbox --pubkey %s\n", sb.name)
	os.Stderr.WriteString(b.String())
	if tty.StdinIsTerminal() {
		tty.ReadLine("Press Enter to open the sandbox... ")
	}
}

// checkPath refuses paths that would hand a sandbox your secrets, or let it
// write among them: used for -w and --cp (what goes in) and for --get (where
// files land). what is e.g. "copy" or "copy files into".
func (a *app) checkPath(path, what string) error {
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
func (a *app) keyDirs() []string {
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
