package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/run"
)

// fakeEngine is a backend.Engine with fixed sandboxes, for code that only
// reads them.
type fakeEngine struct {
	backend.Engine
	rows []backend.Row
}

func (f *fakeEngine) Rows(string) ([]backend.Row, error) { return f.rows, nil }

func TestSplitListLikeBashRead(t *testing.T) {
	for in, want := range map[string]string{"a,b": "a|b", "a,": "a", "a,,": "a|", ",a": "|a", "": "", ",": "", "a,,b,": "a||b"} {
		if got := strings.Join(splitList(in), "|"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if n := len(splitList(",")); n != 1 {
		t.Errorf(`"," is one empty item in bash, got %d items`, n)
	}
}

func TestChoice(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "3": 3, "4": 0, "0": 0, "01": 0, "x": 0, "": 0, "99999999999999999999": 0} {
		n, ok := choice(in, 3)
		if (ok && n != want) || (!ok && want != 0) {
			t.Errorf("%q: got %d %v, want %d", in, n, ok, want)
		}
	}
}

func TestFriendlyState(t *testing.T) {
	for in, want := range map[string]string{
		"Up 5 minutes":                        "open (5 minutes)",
		"Up About a minute":                   "open (1 minute)",
		"Up Less than a second":               "open (just now)",
		"Exited (0) 2 hours ago":              "closed 2 hours ago",
		"Exited (137) Less than a second ago": "closed just now",
		"Exited (0) About an hour ago":        "closed 1 hour ago",
		"Created":                             "new",
		"Dead":                                "Dead",
	} {
		if got := friendlyState(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if got := friendlyFix("Less than a second ago"); got != "just now" {
		t.Errorf("created column: got %q", got)
	}
}

func TestCheckPath(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	clone := filepath.Join(home, "src/opencode-sandbox")
	t.Setenv("OPENCODE_SANDBOX_LEGACY_DIR", clone)
	a := &app{engines: []backend.Engine{&fakeEngine{rows: []backend.Row{{KeyKind: "disposable", Key: "/srv/old-clone/keys/x"}}}}}

	refused := []string{"/", home, filepath.Dir(home), home + "/.ssh", home + "/.ssh/sub", home + "/.local/share/opencode-sandbox",
		clone, clone + "/keys", filepath.Join(home, "src"), "/srv/old-clone/keys", "/srv/old-clone", "/srv/old-clone/keys/x"}
	allowed := []string{home + "/work", home + "/.sshx", home + "/src/other", "/srv/elsewhere"}
	for _, p := range refused {
		if a.checkPath(p, "copy") == nil {
			t.Errorf("%s was allowed", p)
		}
	}
	for _, p := range allowed {
		if err := a.checkPath(p, "copy"); err != nil {
			t.Errorf("%s was refused: %v", p, err)
		}
	}
}

func TestDiscardKeyOnlyTouchesItsOwnKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a := &app{r: run.OS{}}
	d := t.TempDir()
	keygen := func(path, comment string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	gone := func(p string) bool { _, err := os.Lstat(p); return os.IsNotExist(err) }

	current := filepath.Join(d, "keys/demo/id_ed25519")
	keygen(current, "opencode-sandbox-demo-2026-11-27")
	a.discardKey("demo", current)
	if !gone(current) || !gone(current+".pub") || !gone(filepath.Dir(current)) {
		t.Error("current-layout key not deleted")
	}

	legacy := filepath.Join(d, "clone/keys/old")
	keygen(legacy, "opencode-sandbox-old-2026-01-01")
	a.discardKey("old", legacy)
	if !gone(legacy) || !gone(legacy+".pub") {
		t.Error("legacy key not deleted")
	}

	// Not ours: wrong comment, wrong place, wrong name.
	mine := filepath.Join(d, "keys/demo2/id_ed25519")
	keygen(mine, "me@laptop")
	a.discardKey("demo2", mine)
	elsewhere := filepath.Join(d, "ssh/demo3")
	keygen(elsewhere, "opencode-sandbox-demo3-2026-11-27")
	a.discardKey("demo3", elsewhere)
	other := filepath.Join(d, "keys/x/id_ed25519")
	keygen(other, "opencode-sandbox-x-2026-11-27")
	a.discardKey("y", other)
	for _, p := range []string{mine, elsewhere, other} {
		if gone(p) {
			t.Errorf("%s was deleted", p)
		}
	}
}

func TestDefaultNameSanitizes(t *testing.T) {
	a := &app{engines: []backend.Engine{existsNever{&fakeEngine{}}}}
	for in, prefix := range map[string]string{"/w/my project": "my-project-", "/w/.hidden": "sb.hidden-", "/w/ok_1.2": "ok_1.2-", "/w/café": "caf--"} {
		got := a.defaultName(in)
		if !strings.HasPrefix(got, prefix) || len(got) != len(prefix)+4 || !validName.MatchString(got) {
			t.Errorf("%q: got %q, want %s + 4 hex digits", in, got, prefix)
		}
	}
}

type existsNever struct{ backend.Engine }

func (existsNever) Exists(string) bool { return false }
