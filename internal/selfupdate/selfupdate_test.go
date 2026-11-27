package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// script is a stand-in for a build: a program that says it's version v.
func script(v string) string {
	return "#!/bin/sh\necho 'opencode-sandbox " + v + " (opencode 1.0.0)'\n"
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// release serves a latest release of version v whose linux/arm64 build is
// body, listed in SHA256SUMS with the checksum of listed.
func release(t *testing.T, v, body, listed string) *Updater {
	t.Helper()
	file := "opencode-sandbox_" + v + "_linux_arm64"
	sums := sum(listed) + "  " + file + "\n" + sum("other") + "  opencode-sandbox_" + v + "_darwin_amd64\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/download/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "opencode-sandbox/0.1.0" {
			t.Errorf("User-Agent: got %q", got)
		}
		fmt.Fprint(w, sums)
	})
	mux.HandleFunc("/download/v"+v+"/"+file, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Updater{Base: srv.URL, Client: srv.Client(), GOOS: "linux", GOARCH: "arm64", Agent: "opencode-sandbox/0.1.0"}
}

// installed makes the program an update replaces, and returns its path.
func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "opencode-sandbox")
	if err := os.WriteFile(exe, []byte(script("0.1.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// untouched checks that a refused update left exe and its folder as they were.
func untouched(t *testing.T, exe string) {
	t.Helper()
	if got, _ := os.ReadFile(exe); string(got) != script("0.1.0") {
		t.Errorf("the program was changed: %q", got)
	}
	left, _ := os.ReadDir(filepath.Dir(exe))
	if len(left) != 1 {
		t.Errorf("files left behind: %v", left)
	}
}

func TestUpdate(t *testing.T) {
	u := release(t, "0.2.0", script("0.2.0"), script("0.2.0"))
	exe := installed(t)
	if err := os.Chmod(exe, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.2.0" || r.File != "opencode-sandbox_0.2.0_linux_arm64" || r.Sum != sum(script("0.2.0")) {
		t.Fatalf("latest: got %+v", r)
	}
	if want := Releases + "/tag/v0.2.0"; r.Page() != want {
		t.Errorf("page: got %s, want %s", r.Page(), want)
	}
	if err := u.Install(context.Background(), r, exe); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil || !strings.HasPrefix(string(out), "opencode-sandbox 0.2.0 ") {
		t.Errorf("the new program: %q, %v", out, err)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode: got %v, want the old program's", fi.Mode().Perm())
	}
	if left, _ := os.ReadDir(filepath.Dir(exe)); len(left) != 1 {
		t.Errorf("files left behind: %v", left)
	}
}

func TestInstallRefuses(t *testing.T) {
	for name, c := range map[string]struct{ body, listed, want string }{
		"another file than the one listed": {script("0.2.0") + "# and more\n", script("0.2.0"), "doesn't match the release's SHA256SUMS"},
		"a program that fails":             {"#!/bin/sh\nexit 3\n", "#!/bin/sh\nexit 3\n", "doesn't run here"},
		"another version than announced":   {script("0.1.5"), script("0.1.5"), `it calls itself "opencode-sandbox 0.1.5 (opencode 1.0.0)"`},
	} {
		t.Run(name, func(t *testing.T) {
			u := release(t, "0.2.0", c.body, c.listed)
			exe := installed(t)
			r, err := u.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = u.Install(context.Background(), r, exe)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Errorf("got %v, want %q", err, c.want)
			}
			untouched(t, exe)
		})
	}
}

func TestInstallWithoutTheBuild(t *testing.T) {
	u := release(t, "0.2.0", script("0.2.0"), script("0.2.0"))
	exe := installed(t)
	err := u.Install(context.Background(), Release{Version: "0.3.0", File: "opencode-sandbox_0.3.0_linux_arm64", Sum: sum("x")}, exe)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("got %v, want a 404", err)
	}
	untouched(t, exe)
}

func TestInstallWhereItCantWrite(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	u := release(t, "0.2.0", script("0.2.0"), script("0.2.0"))
	exe := installed(t)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	r, _ := u.Latest(context.Background())
	var na *NoAccessError
	if err := u.Install(context.Background(), r, exe); !errors.As(err, &na) || na.Dir != dir {
		t.Errorf("got %v, want a NoAccessError for %s", err, dir)
	}
	untouched(t, exe)
}

// A release reached over https must not be fetched from a plain http address
// it redirects to.
func TestNoRedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the plain http server was asked")
	}))
	defer plain.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/SHA256SUMS", http.StatusFound)
	}))
	defer srv.Close()
	u := &Updater{Base: srv.URL, Client: srv.Client(), GOOS: "linux", GOARCH: "arm64"}
	if _, err := u.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't https") {
		t.Errorf("got %v", err)
	}
}

func TestParseSums(t *testing.T) {
	a, b := sum("a"), sum("b")
	good := a + "  opencode-sandbox_1.2.3_linux_amd64\n" + b + " *opencode-sandbox_1.2.3_darwin_arm64\n"
	r, err := parseSums(good+a+"  install.sh\nnot a checksum line at all\n\n", "darwin", "arm64")
	if err != nil || r != (Release{Version: "1.2.3", File: "opencode-sandbox_1.2.3_darwin_arm64", Sum: b}) {
		t.Errorf("got %+v, %v", r, err)
	}
	for name, c := range map[string]struct{ sums, want string }{
		"no build for this system": {good, "has no build for linux/riscv64"},
		"two versions":             {good + a + "  opencode-sandbox_1.2.4_linux_riscv64\n", "names two versions"},
		"nothing":                  {"", "names no opencode-sandbox build"},
		"a page instead":           {"<html>Not Found</html>\n", "names no opencode-sandbox build"},
		"a path in the name":       {a + "  ../opencode-sandbox_1.2.3_linux_riscv64\n", "names no opencode-sandbox build"},
		"a short checksum":         {"abc123  opencode-sandbox_1.2.3_linux_riscv64\n", "names no opencode-sandbox build"},
	} {
		if _, err := parseSums(c.sums, "linux", "riscv64"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.6.1", "0.6.1", 0}, {"0.7.0", "0.6.1", 1}, {"0.6.1", "0.7.0", -1}, {"0.10.0", "0.9.9", 1},
		{"1.0.0", "0.99.99", 1}, {"v0.6.1", "0.6.1", 0}, {"0.6.2-rc1", "0.6.2", 0}, {"0.6", "0.6.0", 0},
		{"0.6.10", "0.6.9", 1},
	} {
		got := Compare(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("Compare(%s, %s) = %d, want sign of %d", c.a, c.b, got, c.want)
		}
	}
}
