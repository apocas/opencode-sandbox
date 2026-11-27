package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadlinkFMatchesReadlink(t *testing.T) {
	if exec.Command("readlink", "-f", "/").Run() != nil {
		t.Skip("no readlink -f here")
	}
	d := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(d, "real/sub"), 0o755))
	must(os.Symlink("real", filepath.Join(d, "link")))
	must(os.Symlink(filepath.Join(d, "real/sub"), filepath.Join(d, "abs")))
	must(os.Symlink("missing-target", filepath.Join(d, "dangling")))
	must(os.Symlink("link/sub", filepath.Join(d, "chain")))
	// GNU readlink -f follows a dangling symlink to its target; macOS's
	// fails. ReadlinkF does what GNU does, so the target is what's checked.
	real, _ := filepath.EvalSymlinks(d)
	if got, err := ReadlinkF(filepath.Join(d, "dangling")); err != nil || got != filepath.Join(real, "missing-target") {
		t.Errorf("dangling: got %s %v", got, err)
	}
	if got, err := ReadlinkF(filepath.Join(d, "chain/new-file")); err != nil || got != filepath.Join(real, "real/sub/new-file") {
		t.Errorf("missing last part: got %s %v", got, err)
	}
	for _, p := range []string{"real", "link", "link/sub", "abs", "chain", "real/../link"} {
		full := filepath.Join(d, p)
		out, err := exec.Command("readlink", "-f", full).Output()
		if err != nil {
			t.Fatalf("readlink -f %s: %v", p, err)
		}
		got, err := ReadlinkF(full)
		if err != nil {
			t.Fatalf("ReadlinkF(%s): %v", p, err)
		}
		if want := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s: got %s, want %s", p, got, want)
		}
	}
	if _, err := ReadlinkF(filepath.Join(d, "nope/deeper")); err == nil {
		t.Error("a missing parent was accepted")
	}
}

func TestRunDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "/var/tmp/")
	if got, want := RunDir(), "/var/tmp/opencode-sandbox-"+strconv.Itoa(os.Getuid()); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := RunDir(); got != "/run/user/1000/opencode-sandbox" {
		t.Errorf("got %s", got)
	}
}
