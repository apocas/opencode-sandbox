// Package state knows where things live: container names and labels,
// per-user runtime files (agents, generated configs) and persistent data
// (disposable keys, seeded host keys).
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	// Prefix turns a sandbox name into its container name.
	Prefix = "opencode-sandbox-"
	// Network is the network every sandbox is on; containers on it can't
	// reach each other (icc off).
	Network = "opencode-sandbox-net"
	// LabelName marks a container as a sandbox and holds its name.
	LabelName = "opencode-sandbox.name"
	// DetachKeys replaces Docker's default (ctrl-p,ctrl-q), which would
	// swallow opencode's ctrl+p palette.
	DetachKeys = "ctrl-],ctrl-]"
)

// Label is the full name of a sandbox label, e.g. Label("keykind").
func Label(key string) string { return "opencode-sandbox." + key }

// Config is your opencode config; sandboxes get filtered copies of it.
func Config() string {
	if c := os.Getenv("OPENCODE_SANDBOX_CONFIG"); c != "" {
		return c
	}
	return filepath.Join(home(), ".config/opencode/opencode.json")
}

// RunDir holds per-user runtime files: $XDG_RUNTIME_DIR/opencode-sandbox, or
// $TMPDIR/opencode-sandbox-UID where that isn't set (as on macOS). Computed
// exactly as the bash launcher did, so both find the same agents and configs.
func RunDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d + "/opencode-sandbox"
	}
	t := os.Getenv("TMPDIR")
	if t == "" {
		t = "/tmp"
	}
	return strings.TrimSuffix(t, "/") + "/opencode-sandbox-" + strconv.Itoa(os.Getuid())
}

// AgentDir holds a sandbox's ssh-agent; only its sock/ subdirectory is
// mounted into the sandbox.
func AgentDir(name string) string { return RunDir() + "/agents/" + name }

// ConfigFile is a sandbox's generated opencode config.
func ConfigFile(name string) string { return RunDir() + "/configs/" + name + ".json" }

// SandboxDir is the folder a sandbox mounts where only folders can be
// mounted (Apple container): its config and the host keys it trusts.
func SandboxDir(name string) string { return RunDir() + "/sandboxes/" + name }

// DataDir holds what must outlive a reboot: disposable keys and the host
// keys sandboxes trust. $XDG_DATA_HOME/opencode-sandbox, by default
// ~/.local/share/opencode-sandbox.
func DataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "opencode-sandbox")
	}
	return filepath.Join(home(), ".local/share/opencode-sandbox")
}

// KeysDir holds disposable keys, one folder per sandbox.
func KeysDir() string { return filepath.Join(DataDir(), "keys") }

// DisposableKey is where a new sandbox's disposable key goes.
func DisposableKey(name string) string { return filepath.Join(KeysDir(), name, "id_ed25519") }

// KnownHosts holds the seeded host keys every sandbox trusts.
func KnownHosts() string { return filepath.Join(DataDir(), "known_hosts") }

func home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// Home is your home directory.
func Home() string { return home() }

// EnsureRunDir creates RunDir. It holds keys' agents and API keys, so it must
// be a real directory that only you own.
func EnsureRunDir() error {
	d := RunDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(d)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || owner(fi) != os.Getuid() {
		return fmt.Errorf("%s isn't a directory owned by you; refusing to use it", d)
	}
	return os.Chmod(d, 0o700)
}

// MkdirPrivate creates dirs (and their parents) and makes each one
// user-only, like `install -d -m 700`.
func MkdirPrivate(dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func owner(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// KeyMode is how a sandbox uses its key: "agent" (host ssh-agent), "file"
// (disposable key stored inside) or "none". Sandboxes created before 0.2.0
// have no keymode label.
func KeyMode(kind, mode string) string {
	switch {
	case mode != "":
		return mode
	case kind == "none":
		return "none"
	default:
		return "agent"
	}
}

// ReadlinkF resolves p like `readlink -f`: every symlink is followed, and
// all but the last component must exist.
func ReadlinkF(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	for hops := 0; hops < 40; hops++ {
		dir, base := filepath.Split(abs)
		rdir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", err
		}
		full := filepath.Join(rdir, base)
		if base == "" {
			return rdir, nil
		}
		fi, err := os.Lstat(full)
		if os.IsNotExist(err) {
			return full, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return full, nil
		}
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(rdir, target)
		}
		abs = filepath.Clean(target)
	}
	return "", fmt.Errorf("too many levels of symbolic links: %s", p)
}
