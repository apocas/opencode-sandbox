// Package setup carries out a health.Plan: it downloads and checks what the
// plan needs, and runs the plan's commands on this terminal.
package setup

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/dl"
	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/run"
	"github.com/apocas/opencode-sandbox/internal/tarx"
)

// Where what's installed comes from.
const (
	AppleReleases  = "https://github.com/apple/container/releases"
	GvisorReleases = "https://storage.googleapis.com/gvisor/releases/release/latest"
)

// Progress is told what Apply is doing.
type Progress interface {
	// Begin and End surround step i of n.
	Begin(i, n int, s health.Step)
	End(i, n int, s health.Step)
	// Command is a command about to run, as you'd type it.
	Command(shown string)
	// Downloaded says how far a download is; total is -1 if unknown.
	Downloaded(done, total int64)
}

// Setup carries out plans.
type Setup struct {
	Run      run.Runner
	Client   *http.Client
	Progress Progress
	// Stdin and Out are the terminal the commands run on (sudo asks for a
	// password there).
	Stdin io.Reader
	Out   io.Writer
	// Machine is GOARCH: which gVisor build to get.
	Machine string
	// Apple and Gvisor are AppleReleases and GvisorReleases (local servers
	// in tests).
	Apple, Gvisor string
	Agent         string
}

// Apply carries out the plan's steps in order and stops at the first that
// fails. Downloads go to a folder of their own, removed afterwards.
func (s *Setup) Apply(ctx context.Context, p health.Plan) error {
	dir, err := os.MkdirTemp("", "opencode-sandbox-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for i, st := range p.Steps {
		s.Progress.Begin(i+1, len(p.Steps), st)
		switch st.Fetch {
		case health.FetchApple:
			err = s.fetchApple(ctx, dir)
		case health.FetchGvisor:
			err = s.fetchGvisor(ctx, dir)
		case "":
		default:
			err = fmt.Errorf("don't know how to get %q", st.Fetch)
		}
		if err != nil {
			return err
		}
		for _, c := range st.Cmds {
			argv := make([]string, len(c))
			for j, a := range c {
				argv[j] = strings.ReplaceAll(a, health.Downloads, dir)
			}
			shown := Shown(c)
			s.Progress.Command(shown)
			code, err := s.Run.Run(&run.Cmd{Args: argv, Stdin: s.Stdin, Stdout: s.Out, Stderr: s.Out})
			switch {
			case err != nil:
				return fmt.Errorf("can't run %s: %w", argv[0], err)
			case code != 0:
				return &FailedError{Cmd: shown, Code: code, Step: i + 1, Steps: len(p.Steps)}
			}
		}
		s.Progress.End(i+1, len(p.Steps), st)
	}
	return nil
}

// FailedError is a command of a plan that failed, after saying why itself.
type FailedError struct {
	Cmd         string
	Code        int
	Step, Steps int
}

func (e *FailedError) Error() string {
	left := ""
	if e.Step < e.Steps {
		left = "; the steps after it weren't done"
	}
	return fmt.Sprintf("`%s` failed (exit status %d)%s", e.Cmd, e.Code, left)
}

// Shown is a plan's command as you'd type it: what's downloaded is named
// without its folder.
func Shown(c []string) string {
	argv := make([]string, len(c))
	for i, a := range c {
		argv[i] = strings.ReplaceAll(a, health.Downloads+"/", "")
	}
	return run.Quote(argv)
}

var tagName = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// fetchApple downloads the installer of Apple container's latest release
// into dir as health.ApplePkg, and checks that Apple signed it.
func (s *Setup) fetchApple(ctx context.Context, dir string) error {
	tag, err := s.latestTag(ctx, s.Apple+"/latest")
	if err != nil {
		return fmt.Errorf("can't find Apple container's latest release: %w", err)
	}
	pkg := filepath.Join(dir, health.ApplePkg)
	url := fmt.Sprintf("%s/download/%s/container-%s-installer-signed.pkg", s.Apple, tag, tag)
	if err := dl.File(ctx, s.Client, url, s.Agent, pkg, 2<<30, nil, s.Progress.Downloaded); err != nil {
		return err
	}
	var out bytes.Buffer
	code, err := s.Run.Run(&run.Cmd{Args: []string{"/usr/sbin/pkgutil", "--check-signature", pkg}, Stdout: &out})
	switch {
	case err != nil:
		return fmt.Errorf("can't check the installer's signature: %w", err)
	case code != 0 || !AppleSigned(out.String()):
		return fmt.Errorf("the installer downloaded from %s isn't signed by Apple, so it wasn't run; nothing was installed", url)
	}
	return nil
}

// latestTag asks GitHub which release "latest" is: it answers with a redirect
// to the release's page.
func (s *Setup) latestTag(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if s.Agent != "" {
		req.Header.Set("User-Agent", s.Agent)
	}
	c := *s.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/releases/tag/")
	if resp.StatusCode/100 != 3 || i < 0 {
		return "", fmt.Errorf("%s: %s, not a redirect to a release", url, resp.Status)
	}
	tag := loc[i+len("/releases/tag/"):]
	if !tagName.MatchString(tag) {
		return "", fmt.Errorf("%s: an odd release name, %q", url, tag)
	}
	return tag, nil
}

var (
	appleStatus = regexp.MustCompile(`(?m)^\s*Status: signed by a developer certificate issued by Apple for distribution\s*$`)
	appleSigner = regexp.MustCompile(`(?m)^\s*1\. Developer ID Installer: Apple Inc\. - `)
)

// AppleSigned reads what `pkgutil --check-signature` says about a package:
// true if its signature is valid and its signer is Apple itself.
func AppleSigned(out string) bool {
	return appleStatus.MatchString(out) && appleSigner.MatchString(out)
}

var sha512Hex = regexp.MustCompile(`^[0-9a-f]{128}$`)

// fetchGvisor downloads gVisor's latest release for this machine, checks it
// against the SHA-512 published with it, and unpacks it into dir: runsc,
// containerd-shim-runsc-v1 and the folder gvisor-bin.
func (s *Setup) fetchGvisor(ctx context.Context, dir string) error {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[s.Machine]
	if arch == "" {
		return fmt.Errorf("gVisor has no build for %s", s.Machine)
	}
	url := s.Gvisor + "/" + arch + "/gvisor.tar.bz2"
	text, err := dl.Text(ctx, s.Client, url+".sha512", s.Agent, 4096)
	if err != nil {
		return err
	}
	want, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	if !sha512Hex.MatchString(want) {
		return fmt.Errorf("%s.sha512 isn't a SHA-512 checksum", url)
	}
	archive := filepath.Join(dir, "gvisor.tar.bz2")
	h := sha512.New()
	if err := dl.File(ctx, s.Client, url, s.Agent, archive, 1<<30, h, s.Progress.Downloaded); err != nil {
		return err
	}
	defer os.Remove(archive)
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("the gVisor downloaded from %s doesn't match its published SHA-512 (got %s…, want %s…); nothing was installed", url, got[:16], want[:16])
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tarx.Extract(bzip2.NewReader(f), dir); err != nil {
		return fmt.Errorf("unpacking gVisor: %w", err)
	}
	for _, name := range []string{"runsc", "containerd-shim-runsc-v1"} {
		if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || !fi.Mode().IsRegular() {
			return errors.New("gVisor's release doesn't have " + name + " where it used to; nothing was installed")
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, "gvisor-bin")); err != nil || !fi.IsDir() {
		return errors.New("gVisor's release doesn't have the folder gvisor-bin where it used to; nothing was installed")
	}
	return nil
}
