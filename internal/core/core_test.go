package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/platform"
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
		if _, got := friendlyState(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]State{"Up 5 minutes": StateOpen, "Up": StateOpen, "Exited (0) 2 hours ago": StateClosed,
		"Exited": StateClosed, "Created": StateNew, "Dead": StateOther} {
		if got, _ := friendlyState(in); got != want {
			t.Errorf("%q: state %v, want %v", in, got, want)
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
	a := &Core{engines: []backend.Engine{&fakeEngine{rows: []backend.Row{{KeyKind: "disposable", Key: "/srv/old-clone/keys/x"}}}}}

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
	a := &Core{r: run.OS{}}
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
	a := &Core{engines: []backend.Engine{existsNever{&fakeEngine{}}}}
	for in, prefix := range map[string]string{"/w/my project": "my-project-", "/w/.hidden": "sb.hidden-", "/w/ok_1.2": "ok_1.2-", "/w/café": "caf--"} {
		got := a.DefaultName(in)
		if !strings.HasPrefix(got, prefix) || len(got) != len(prefix)+4 || !validName.MatchString(got) {
			t.Errorf("%q: got %q, want %s + 4 hex digits", in, got, prefix)
		}
	}
}

type existsNever struct{ backend.Engine }

func (existsNever) Exists(string) bool { return false }

// named is an engine of which only the name matters.
type named struct {
	backend.Engine
	name string
}

func (n named) Name() string { return n.name }

func (n named) Info() (backend.Info, error) { return backend.Info{}, nil }

func TestPlace(t *testing.T) {
	for _, v := range []string{"OPENCODE_SANDBOX_ENGINE", "OPENCODE_SANDBOX_RUNTIME", "OPENCODE_SANDBOX_NO_AGENT"} {
		t.Setenv(v, "")
	}
	info := backend.Info{DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{
		"runc": {Path: "runc"}, "runsc": {Path: "/usr/local/bin/runsc"},
		"runsc-ssh": {Path: "/usr/local/bin/runsc", Args: []string{"--host-uds=open"}},
	}}
	both := &Core{engines: []backend.Engine{named{name: "docker"}, named{name: "apple"}}, dockerInfo: info}
	docker := &Core{engines: []backend.Engine{named{name: "docker"}}, dockerInfo: info}
	cases := []struct {
		name          string
		c             *Core
		engine, rt    string
		env           [2]string // a variable and its value
		wantE, wantRT string
		wantErr       string
		wantNotInErr  string
	}{
		{name: "by itself, Apple container where it runs", c: both, wantE: "apple"},
		{name: "by itself, Docker elsewhere", c: docker, wantE: "docker"},
		{name: "Docker asked for", c: both, engine: "docker", wantE: "docker"},
		{name: "a runtime asked for", c: both, engine: "docker", rt: "runsc", wantE: "docker", wantRT: "runsc"},
		{name: "the environment's engine", c: both, env: [2]string{"OPENCODE_SANDBOX_ENGINE", "docker"}, wantE: "docker"},
		{name: "the option wins over the environment", c: both, engine: "apple", env: [2]string{"OPENCODE_SANDBOX_ENGINE", "docker"}, wantE: "apple"},
		{name: "the environment's runtime", c: docker, env: [2]string{"OPENCODE_SANDBOX_RUNTIME", "runsc"}, wantE: "docker", wantRT: "runsc"},
		// What can't be: the error names the option that asked, or else the variable.
		{name: "an engine that isn't one", c: both, engine: "podman", wantErr: `--engine must be docker or apple, not "podman"`},
		{name: "an engine that isn't running", c: docker, engine: "apple", wantErr: "--engine apple, but Apple container isn't running here", wantNotInErr: "OPENCODE_SANDBOX_ENGINE"},
		{name: "the environment's engine isn't running", c: docker, env: [2]string{"OPENCODE_SANDBOX_ENGINE", "apple"}, wantErr: "OPENCODE_SANDBOX_ENGINE=apple, but Apple container isn't running here"},
		{name: "a runtime Docker hasn't", c: docker, rt: "nope", wantErr: "--runtime nope, but Docker has no runtime by that name (it has: runc, runsc, runsc-ssh)", wantNotInErr: "OPENCODE_SANDBOX_RUNTIME"},
		{name: "the environment's runtime Docker hasn't", c: docker, env: [2]string{"OPENCODE_SANDBOX_RUNTIME", "nope"}, wantErr: "OPENCODE_SANDBOX_RUNTIME=nope, but Docker has no runtime"},
		{name: "a runtime where it goes to Apple container", c: both, rt: "runsc", wantErr: "--runtime runsc chooses a Docker runtime, but the sandbox would go to Apple container; add --engine docker"},
		{name: "the environment's runtime where it goes to Apple container", c: both, env: [2]string{"OPENCODE_SANDBOX_RUNTIME", "runsc"},
			wantErr: "OPENCODE_SANDBOX_RUNTIME=runsc chooses a Docker runtime, but new sandboxes go to Apple container; set OPENCODE_SANDBOX_ENGINE=docker to use Docker"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env[0] != "" {
				t.Setenv(c.env[0], c.env[1])
			}
			e, plat, err := c.c.place(c.engine, c.rt)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || c.wantNotInErr != "" && strings.Contains(err.Error(), c.wantNotInErr) {
					t.Fatalf("error %v, want %q", err, c.wantErr)
				}
				if c.c.CheckPlace(c.engine, c.rt) == nil {
					t.Error("CheckPlace finds nothing wrong")
				}
				return
			}
			// (Left to itself, the runtime depends on the system the test runs on.)
			if err != nil || e.Name() != c.wantE || c.rt+c.env[1] != "" && c.wantRT != "" && plat.Runtime != c.wantRT {
				t.Errorf("engine %v, runtime %q, error %v; want %s and %q", e, plat.Runtime, err, c.wantE, c.wantRT)
			}
		})
	}
}

func TestPlaces(t *testing.T) {
	for _, v := range []string{"OPENCODE_SANDBOX_ENGINE", "OPENCODE_SANDBOX_RUNTIME", "OPENCODE_SANDBOX_NO_AGENT"} {
		t.Setenv(v, "")
	}
	info := backend.Info{OS: "Ubuntu 24.04", DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{
		"runc": {Path: "runc"}, "io.containerd.runc.v2": {Path: "runc"}, "runsc": {Path: "/usr/local/bin/runsc"},
		"gone": {Path: "/usr/local/bin/runsc", Missing: true},
	}}
	// The first is where it goes by itself; the order of the rest, and
	// what a plain container shares, depend on the system the test runs on.
	shared := "on this machine's kernel"
	if platform.InVM(info) {
		shared = "in the VM your containers share"
	}
	list := func(a *Core) string {
		var out []string
		for _, p := range a.Places() {
			out = append(out, p.Engine+"|"+p.Runtime+" "+p.Label+": "+strings.Replace(p.Detail, shared, "SHARED", 1))
		}
		if len(out) > 1 {
			sort.Strings(out[1:])
		}
		return strings.Join(out, "\n")
	}
	both := &Core{engines: []backend.Engine{named{name: "docker"}, named{name: "apple"}}, dockerInfo: info}
	want := strings.Join([]string{
		"| automatic: Apple container: a VM of its own",
		"apple| Apple container: a VM of its own",
		"docker|runc Docker: a hardened container SHARED (runtime runc)",
		"docker|runsc Docker with gVisor: a kernel of its own (runtime runsc)",
	}, "\n")
	if got := list(both); got != want {
		t.Errorf("places:\n%s\nwant:\n%s", got, want)
	}
	// Docker alone, without gVisor: by itself, and the same asked for.
	plain := &Core{engines: []backend.Engine{named{name: "docker"}},
		dockerInfo: backend.Info{DefaultRuntime: "runc", Runtimes: map[string]backend.Runtime{"runc": {Path: "runc"}}}}
	want = "| automatic: Docker: a hardened container SHARED (runtime runc)\n" +
		"docker|runc Docker: a hardened container SHARED (runtime runc)"
	if got := list(plain); got != want {
		t.Errorf("places:\n%s\nwant:\n%s", got, want)
	}
	if got := list(&Core{}); got != "" {
		t.Errorf("places where nothing runs: %q", got)
	}
}

func TestNetworkAndGatewayAskedFor(t *testing.T) {
	t.Setenv(NetworkEnv, "")
	t.Setenv("OPENCODE_SANDBOX_GATEWAY", "")
	for _, c := range []struct{ want, env, got, err string }{
		{"", "", "open", ""}, {"open", "none", "open", ""}, {"none", "", "none", ""}, {"", "none", "none", ""}, {"", "open", "open", ""},
		{"sideways", "", "", `--network must be open or none, not "sideways"`}, {"", "off", "", `OPENCODE_SANDBOX_NETWORK must be open or none, not "off"`},
	} {
		t.Setenv(NetworkEnv, c.env)
		got, err := NetworkFor(c.want)
		if got != c.got || (err == nil) != (c.err == "") || err != nil && err.Error() != c.err {
			t.Errorf("asked %q with %s=%q: %q, %v; want %q, %q", c.want, NetworkEnv, c.env, got, err, c.got, c.err)
		}
	}
	for _, c := range []struct {
		want, env string
		got, ok   bool
	}{{"", "", true, true}, {"", "off", false, true}, {"on", "off", true, true}, {"off", "", false, true}, {"maybe", "", false, false}} {
		t.Setenv("OPENCODE_SANDBOX_GATEWAY", c.env)
		if got, err := gatewayFor(c.want); got != c.got || (err == nil) != c.ok {
			t.Errorf("gateway asked %q with the variable %q: %v, %v", c.want, c.env, got, err)
		}
	}
	// A sandbox without a network: no gateway off, no key, before anything is made.
	a := &Core{engines: []backend.Engine{named{name: "apple"}}, Rep: Plain{}}
	t.Setenv(NetworkEnv, "")
	for _, c := range []struct {
		o    CreateOptions
		want string
	}{
		{CreateOptions{Network: "none", Gateway: "off"}, "can't be made with the gateway off"},
		{CreateOptions{Network: "none", Key: "new"}, "so it gets no SSH key; leave -k out"},
		{CreateOptions{Network: "ether"}, "--network must be open or none"},
		{CreateOptions{Gateway: "maybe"}, "--gateway must be on or off"},
		{CreateOptions{Engine: "docker"}, "--engine docker, but"},
	} {
		if _, err := a.Create(c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.o, err, c.want)
		}
	}
}

// A sandbox that starts empty copies no folder, not the current one.
func TestEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OPENCODE_SANDBOX_LEGACY_DIR", "")
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	t.Chdir(dir)
	t.Setenv("PWD", dir)
	a := &Core{engines: []backend.Engine{existsNever{&fakeEngine{}}}}
	both := &Core{engines: []backend.Engine{named{name: "apple"}}, Rep: Plain{}}
	if _, err := both.Create(CreateOptions{Empty: true, Workspace: dir, Key: "none"}); err == nil || !strings.Contains(err.Error(), "a copy of a folder or starts empty, not both") {
		t.Errorf("a folder and empty: %v", err)
	}
	if ws, err := a.ResolveWorkspace(""); ws != dir || err != nil {
		t.Errorf("no folder named: %q, %v; want the current one", ws, err)
	}
	if name := a.DefaultName(""); !strings.HasPrefix(name, "sandbox-") || len(name) != len("sandbox-")+4 || !validName.MatchString(name) {
		t.Errorf("the name of a sandbox that copies nothing: %q", name)
	}
	if d := a.DotFiles(""); len(d.Found) != 0 || d.Warning("") != "" {
		t.Errorf("dot files of no folder: %+v", d)
	}
}
