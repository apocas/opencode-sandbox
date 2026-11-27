// Package selfupdate updates the opencode-sandbox program itself: a release
// binary is replaced by the latest release's, a git clone is pulled.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/apocas/opencode-sandbox/internal/dl"
)

// Releases is where opencode-sandbox's releases are published.
const Releases = "https://github.com/apocas/opencode-sandbox/releases"

const program = "opencode-sandbox"

// maxSize is more than any build will be; a download that goes past it is
// stopped.
const maxSize = 256 << 20

// Updater finds and installs releases.
type Updater struct {
	// Base is Releases (a local server in tests).
	Base   string
	Client *http.Client
	// GOOS and GOARCH choose the build.
	GOOS, GOARCH string
	// Agent is sent as the User-Agent.
	Agent string
}

// New makes an Updater for the system this program was built for; version is
// the running one.
func New(version string) *Updater {
	return &Updater{Base: Releases, Client: &http.Client{}, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Agent: program + "/" + version}
}

// Release is a published version and its build for this system.
type Release struct {
	Version string
	// File is the build's name in the release, Sum its SHA-256 in hex.
	File, Sum string
}

// Page is where the release and what changed in it are shown.
func (r Release) Page() string { return Releases + "/tag/v" + r.Version }

// Latest asks for the latest release. Its SHA256SUMS names the version and
// every build, so one small download answers "is there something newer?".
func (u *Updater) Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	body, err := u.open(ctx, u.Base+"/latest/download/SHA256SUMS")
	if err != nil {
		return Release{}, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return Release{}, err
	}
	return parseSums(string(data), u.GOOS, u.GOARCH)
}

var (
	asset  = regexp.MustCompile(`^opencode-sandbox_(\d+\.\d+\.\d+)_([a-z0-9]+)_([a-z0-9]+)$`)
	hexSum = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// parseSums reads a release's SHA256SUMS. Lines about anything but a build
// are skipped, so a later release can list more.
func parseSums(data, goos, goarch string) (Release, error) {
	var r Release
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		m := asset.FindStringSubmatch(name)
		if m == nil || !hexSum.MatchString(f[0]) {
			continue
		}
		switch {
		case r.Version == "":
			r.Version = m[1]
		case r.Version != m[1]:
			return Release{}, fmt.Errorf("the release's SHA256SUMS names two versions (%s and %s)", r.Version, m[1])
		}
		if m[2] == goos && m[3] == goarch {
			r.File, r.Sum = name, f[0]
		}
	}
	switch {
	case r.Version == "":
		return Release{}, errors.New("the release's SHA256SUMS names no opencode-sandbox build")
	case r.File == "":
		return Release{}, fmt.Errorf("opencode-sandbox %s has no build for %s/%s", r.Version, goos, goarch)
	}
	return r, nil
}

// Install downloads r's build, checks it against the release's SHA256SUMS and
// that it runs, and puts it in place of exe. Nothing changes until the last
// step, which replaces exe at once: a failure never leaves half a program.
func (u *Updater) Install(ctx context.Context, r Release, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	dir := filepath.Dir(exe)
	old, statErr := os.Stat(exe)
	tmp, err := os.CreateTemp(dir, "."+program+"-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return &NoAccessError{Dir: dir}
		}
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // gone already once it's in place

	body, err := u.open(ctx, u.Base+"/download/v"+r.Version+"/"+r.File)
	if err != nil {
		tmp.Close()
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, maxSize+1))
	body.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("downloading %s: %w", r.File, err)
	case n > maxSize:
		return fmt.Errorf("%s is larger than %d MiB, which no build is; nothing was changed", r.File, maxSize>>20)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.Sum {
		return fmt.Errorf("the download doesn't match the release's SHA256SUMS (got %s, want %s); nothing was changed", got, r.Sum)
	}

	mode := fs.FileMode(0o755)
	if statErr == nil {
		mode = old.Mode().Perm() | 0o100
		// Updated with sudo? Then it stays its owner's.
		if st, ok := old.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(name, int(st.Uid), int(st.Gid))
		}
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := runs(ctx, name, r.Version); err != nil {
		return fmt.Errorf("the downloaded opencode-sandbox %s doesn't run here (%v); nothing was changed", r.Version, err)
	}
	return os.Rename(name, exe)
}

// NoAccessError: the folder the program is in can't be written to.
type NoAccessError struct{ Dir string }

func (e *NoAccessError) Error() string { return "can't write to " + e.Dir }

// runs checks that the program at path starts and is the version expected.
func runs(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	if !strings.HasPrefix(line, program+" "+version+" ") {
		return fmt.Errorf("it calls itself %q", line)
	}
	return nil
}

// open starts a download; the caller closes it.
func (u *Updater) open(ctx context.Context, url string) (io.ReadCloser, error) {
	body, _, err := dl.Open(ctx, u.Client, url, u.Agent)
	return body, err
}

// Compare orders two versions (X.Y.Z, a leading "v" and anything after "-" or
// "+" ignored): negative if a is older than b, 0 if equal, positive if newer.
func Compare(a, b string) int {
	x, y := numbers(a), numbers(b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] - y[i]
		}
	}
	return 0
}

func numbers(v string) [3]int {
	var n [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		n[i], _ = strconv.Atoi(p)
	}
	return n
}

// Executable is the program that's running, symlinks resolved: the file an
// update replaces.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
