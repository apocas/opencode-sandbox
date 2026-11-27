package setup

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/health"
	"github.com/apocas/opencode-sandbox/internal/run"
)

// What `pkgutil --check-signature` says about Apple's installer.
const signedByApple = `Package "container-installer.pkg":
   Status: signed by a developer certificate issued by Apple for distribution
   Notarization: trusted by the Apple notary service
   Signed with a trusted timestamp on: 2026-11-07 00:10:04 +0000
   Certificate Chain:
    1. Developer ID Installer: Apple Inc. - Containerization (UPBK2H6LZM)
       Expires: 2030-06-04 16:44:29 +0000
       ------------------------------------------------------------------------
    2. Developer ID Certification Authority
       Expires: 2031-09-17 00:00:00 +0000
       ------------------------------------------------------------------------
    3. Apple Root CA
       Expires: 2035-02-09 21:40:36 +0000
`

func TestAppleSigned(t *testing.T) {
	if !AppleSigned(signedByApple) {
		t.Error("Apple's own installer was refused")
	}
	for name, out := range map[string]string{
		"unsigned":             "Package \"x.pkg\":\n   Status: no signature\n",
		"nothing":              "",
		"another developer":    strings.Replace(signedByApple, "Apple Inc. - Containerization (UPBK2H6LZM)", "Example Corp (ABCDE12345)", 1),
		"a name with Apple in": strings.Replace(signedByApple, "Apple Inc. - Containerization", "Not Apple Inc. - Containerization", 1),
		"Apple further down the chain": strings.Replace(strings.Replace(signedByApple,
			"1. Developer ID Installer: Apple Inc. - Containerization (UPBK2H6LZM)", "1. Developer ID Installer: Example Corp (ABCDE12345)", 1),
			"2. Developer ID Certification Authority", "2. Developer ID Installer: Apple Inc. - Containerization", 1),
		"an expired or untrusted signature": strings.Replace(signedByApple,
			"signed by a developer certificate issued by Apple for distribution", "signed by a certificate that has since expired", 1),
	} {
		if AppleSigned(out) {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fake stands in for the programs setup runs. It notes each command, with
// the downloads folder shown as DL, and whether the files a command names in
// that folder were there.
type fake struct {
	t       *testing.T
	pkgutil string // what pkgutil says
	fail    string // a command that fails: its first words
	calls   []string
}

func (f *fake) Run(c *run.Cmd) (int, error) {
	var shown []string
	for _, a := range c.Args {
		if i := strings.Index(a, "opencode-sandbox-setup-"); i >= 0 {
			if _, err := os.Lstat(a); err != nil {
				f.t.Errorf("%s names %s, which isn't there", c.Args[0], a)
			}
			rest := a[i:]
			a = "DL"
			if j := strings.IndexByte(rest, filepath.Separator); j >= 0 {
				a += rest[j:]
			}
		}
		shown = append(shown, a)
	}
	line := strings.Join(shown, " ")
	f.calls = append(f.calls, line)
	if c.Args[0] == "/usr/sbin/pkgutil" {
		io.WriteString(c.Stdout, f.pkgutil)
		return 0, nil
	}
	if f.fail != "" && strings.HasPrefix(line, f.fail) {
		return 3, nil
	}
	return 0, nil
}

// steps notes what Progress is told.
type steps struct{ log []string }

func (s *steps) Begin(i, n int, st health.Step) {
	s.log = append(s.log, fmt.Sprintf("begin %d/%d", i, n))
}
func (s *steps) End(i, n int, st health.Step) { s.log = append(s.log, fmt.Sprintf("end %d/%d", i, n)) }
func (s *steps) Command(shown string)         { s.log = append(s.log, "$ "+shown) }
func (s *steps) Downloaded(done, total int64) {}

// sources serves stand-ins for Apple's and gVisor's releases.
func sources(t *testing.T, pkg string, gvisorSum func(real string) string) (*Setup, *fake, *steps) {
	t.Helper()
	archive, err := os.ReadFile("testdata/gvisor.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	h := sha512.Sum512(archive)
	sum := hex.EncodeToString(h[:])
	if gvisorSum != nil {
		sum = gvisorSum(sum)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/apple/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/apple/releases/tag/1.2.3", http.StatusFound)
	})
	mux.HandleFunc("/apple/releases/download/1.2.3/container-1.2.3-installer-signed.pkg", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, pkg)
	})
	mux.HandleFunc("/gvisor/aarch64/gvisor.tar.bz2", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/gvisor/aarch64/gvisor.tar.bz2.sha512", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  gvisor.tar.bz2\n", sum)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f, p := &fake{t: t, pkgutil: signedByApple}, &steps{}
	return &Setup{Run: f, Client: srv.Client(), Progress: p, Machine: "arm64",
		Apple: srv.URL + "/apple/releases", Gvisor: srv.URL + "/gvisor"}, f, p
}

// leftovers lists the download folders still there.
func leftovers(t *testing.T) []string {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "opencode-sandbox-setup-*"))
	return left
}

var applePlan = health.Plan{What: "Apple container", Steps: []health.Step{
	{Title: "download", Fetch: health.FetchApple},
	{Title: "install", Cmds: [][]string{{"sudo", "/usr/sbin/installer", "-pkg", health.Downloads + "/" + health.ApplePkg, "-target", "/"}}},
	{Title: "start", Cmds: [][]string{{"/usr/local/bin/container", "system", "start", "--enable-kernel-install"}}},
}}

var gvisorPlan = health.Plan{What: "gVisor", Steps: []health.Step{
	{Title: "download", Fetch: health.FetchGvisor},
	{Title: "install", Cmds: [][]string{
		{"sudo", "install", "-m", "755", health.Downloads + "/runsc", health.Downloads + "/containerd-shim-runsc-v1", "/usr/local/bin/"},
		{"sudo", "cp", "-R", health.Downloads + "/gvisor-bin", "/usr/local/bin/"},
	}},
	{Title: "register", Cmds: [][]string{{"sudo", "/usr/local/bin/runsc", "install"}}},
	{Title: "reload", Cmds: [][]string{{"sudo", "systemctl", "reload", "docker"}}},
}}

func TestApple(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, f, p := sources(t, "the installer", nil)
	if err := s.Apply(context.Background(), applePlan); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/sbin/pkgutil --check-signature DL/container-installer.pkg",
		"sudo /usr/sbin/installer -pkg DL/container-installer.pkg -target /",
		"/usr/local/bin/container system start --enable-kernel-install",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	wantLog := []string{"begin 1/3", "end 1/3", "begin 2/3", "$ sudo /usr/sbin/installer -pkg container-installer.pkg -target /", "end 2/3",
		"begin 3/3", "$ /usr/local/bin/container system start --enable-kernel-install", "end 3/3"}
	if strings.Join(p.log, "\n") != strings.Join(wantLog, "\n") {
		t.Errorf("progress:\n%s\nwant:\n%s", strings.Join(p.log, "\n"), strings.Join(wantLog, "\n"))
	}
	if left := leftovers(t); len(left) > 0 {
		t.Errorf("downloads left behind: %v", left)
	}
}

func TestAppleNotSignedByApple(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, f, _ := sources(t, "something else", nil)
	f.pkgutil = "Package \"container-installer.pkg\":\n   Status: no signature\n"
	err := s.Apply(context.Background(), applePlan)
	if err == nil || !strings.Contains(err.Error(), "isn't signed by Apple") || !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("got %v", err)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "/usr/sbin/pkgutil ") {
		t.Errorf("ran %v, want only the signature check", f.calls)
	}
	if left := leftovers(t); len(left) > 0 {
		t.Errorf("downloads left behind: %v", left)
	}
}

func TestGvisor(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, f, _ := sources(t, "", nil)
	if err := s.Apply(context.Background(), gvisorPlan); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sudo install -m 755 DL/runsc DL/containerd-shim-runsc-v1 /usr/local/bin/",
		"sudo cp -R DL/gvisor-bin /usr/local/bin/",
		"sudo /usr/local/bin/runsc install",
		"sudo systemctl reload docker",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if left := leftovers(t); len(left) > 0 {
		t.Errorf("downloads left behind: %v", left)
	}
}

func TestGvisorRefused(t *testing.T) {
	for name, c := range map[string]struct {
		sum     func(string) string
		machine string
		want    string
	}{
		"another archive than the one published": {func(s string) string { return strings.Repeat("0", 128) }, "arm64", "doesn't match its published SHA-512"},
		"no checksum published":                  {func(s string) string { return "<html>" }, "arm64", "isn't a SHA-512 checksum"},
		"no build for the machine":               {nil, "riscv64", "gVisor has no build for riscv64"},
		"no build served":                        {nil, "amd64", "404"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			s, f, _ := sources(t, "", c.sum)
			s.Machine = c.machine
			err := s.Apply(context.Background(), gvisorPlan)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("ran %v, want nothing", f.calls)
			}
			if left := leftovers(t); len(left) > 0 {
				t.Errorf("downloads left behind: %v", left)
			}
		})
	}
}

func TestAStepThatFails(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, f, p := sources(t, "", nil)
	f.fail = "sudo cp"
	err := s.Apply(context.Background(), gvisorPlan)
	var fe *FailedError
	if !errors.As(err, &fe) || fe.Code != 3 || fe.Step != 2 ||
		err.Error() != "`sudo cp -R gvisor-bin /usr/local/bin/` failed (exit status 3); the steps after it weren't done" {
		t.Errorf("got %v", err)
	}
	if got := f.calls[len(f.calls)-1]; got != "sudo cp -R DL/gvisor-bin /usr/local/bin/" {
		t.Errorf("went on after the failure: %v", f.calls)
	}
	if last := p.log[len(p.log)-1]; last != "$ sudo cp -R gvisor-bin /usr/local/bin/" {
		t.Errorf("progress after the failure: %v", p.log)
	}
	f.fail, f.calls = "sudo systemctl", nil
	if err := s.Apply(context.Background(), gvisorPlan); err == nil || strings.Contains(err.Error(), "steps after it") {
		t.Errorf("the last step failing: got %v", err)
	}
}

func TestLatestTag(t *testing.T) {
	for name, c := range map[string]struct {
		location string
		status   int
		want     string
		ok       bool
	}{
		"a release":        {"https://github.com/apple/container/releases/tag/1.7.1", 302, "1.7.1", true},
		"no redirect":      {"", 200, "", false},
		"somewhere else":   {"https://example.com/login", 302, "", false},
		"a path for a tag": {"https://github.com/apple/container/releases/tag/../../evil", 302, "", false},
		"an empty tag":     {"https://github.com/apple/container/releases/tag/", 302, "", false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.location != "" {
				w.Header().Set("Location", c.location)
			}
			w.WriteHeader(c.status)
		}))
		s := &Setup{Client: srv.Client()}
		got, err := s.latestTag(context.Background(), srv.URL)
		srv.Close()
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

// The real things, when asked for (they download some 250 MB):
// OPENCODE_SANDBOX_TEST_DOWNLOADS=1 go test ./internal/setup -run Real -v
func TestRealDownloads(t *testing.T) {
	if os.Getenv("OPENCODE_SANDBOX_TEST_DOWNLOADS") == "" {
		t.Skip("set OPENCODE_SANDBOX_TEST_DOWNLOADS=1 to download Apple's installer and gVisor")
	}
	s := &Setup{Run: run.OS{}, Client: &http.Client{}, Progress: &steps{}, Apple: AppleReleases, Gvisor: GvisorReleases}
	for _, machine := range []string{"amd64", "arm64"} {
		dir := t.TempDir()
		s.Machine = machine
		if err := s.fetchGvisor(context.Background(), dir); err != nil {
			t.Errorf("gVisor for %s: %v", machine, err)
			continue
		}
		for _, name := range []string{"runsc", "containerd-shim-runsc-v1", "gvisor-bin/gvisor_sentry"} {
			if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm()&0o111 == 0 || fi.Size() < 1<<10 {
				t.Errorf("gVisor for %s: %s: %v, %v", machine, name, fi, err)
			}
		}
	}
	if _, err := os.Stat("/usr/sbin/pkgutil"); err != nil {
		t.Log("no pkgutil here: Apple's installer not checked")
		return
	}
	dir := t.TempDir()
	if err := s.fetchApple(context.Background(), dir); err != nil {
		t.Fatalf("Apple's installer: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, health.ApplePkg)); err != nil || fi.Size() < 50<<20 {
		t.Errorf("Apple's installer: %v, %v", fi, err)
	}
}
