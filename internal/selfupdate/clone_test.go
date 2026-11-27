package selfupdate

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes an upstream repository that looks like opencode-sandbox's and a
// clone of it with a built program, and returns both folders.
func repo(t *testing.T) (upstream, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	// Nothing from the machine's own git setup.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.com")
	}
	dir := t.TempDir()
	upstream, clone = filepath.Join(dir, "upstream"), filepath.Join(dir, "clone")
	git(t, dir, "init", "--quiet", "--initial-branch", "master", upstream)
	write(t, filepath.Join(upstream, "go.mod"), "module github.com/apocas/opencode-sandbox\n\ngo 1.26.0\n", 0o644)
	write(t, filepath.Join(upstream, "VERSION"), "0.1.0\n", 0o644)
	// The clone's script: here it only reports the version in the clone.
	write(t, filepath.Join(upstream, "opencode-sandbox"),
		"#!/bin/sh\necho \"opencode-sandbox $(cat \"$(dirname \"$0\")/VERSION\") (opencode 1.0.0)\"\necho built >&2\n", 0o755)
	git(t, upstream, "add", ".")
	git(t, upstream, "commit", "--quiet", "-m", "one")
	git(t, dir, "clone", "--quiet", upstream, clone)
	write(t, filepath.Join(clone, ".bin", "opencode-sandbox"), "built", 0o755)
	return upstream, clone
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// release2 publishes a second commit upstream.
func release2(t *testing.T, upstream string) {
	t.Helper()
	write(t, filepath.Join(upstream, "VERSION"), "0.2.0\n", 0o644)
	git(t, upstream, "commit", "--quiet", "-am", "two")
}

func TestCloneOf(t *testing.T) {
	_, clone := repo(t)
	exe := filepath.Join(clone, ".bin", "opencode-sandbox")
	if c := CloneOf(exe); c == nil || c.Root != clone {
		t.Fatalf("got %+v, want the clone in %s", c, clone)
	}
	// A release binary someone keeps in a folder named .bin, or in a clone
	// of something else.
	other := t.TempDir()
	write(t, filepath.Join(other, ".bin", "opencode-sandbox"), "x", 0o755)
	if c := CloneOf(filepath.Join(other, ".bin", "opencode-sandbox")); c != nil {
		t.Errorf("a folder without a clone: got %+v", c)
	}
	write(t, filepath.Join(other, "go.mod"), "module example.com/else\n", 0o644)
	write(t, filepath.Join(other, ".git", "HEAD"), "x", 0o644)
	if c := CloneOf(filepath.Join(other, ".bin", "opencode-sandbox")); c != nil {
		t.Errorf("another module's clone: got %+v", c)
	}
	if c := CloneOf(filepath.Join(clone, "opencode-sandbox")); c != nil {
		t.Errorf("the clone's script: got %+v", c)
	}
}

func TestClonePull(t *testing.T) {
	upstream, clone := repo(t)
	c := &Clone{Root: clone}
	if b := c.Branch(); b != "master" {
		t.Errorf("branch: got %q", b)
	}
	if n, err := c.Behind(); err != nil || n != 0 {
		t.Errorf("behind, before: got %d, %v", n, err)
	}
	release2(t, upstream)
	if n, err := c.Behind(); err != nil || n != 1 {
		t.Errorf("behind, after a commit upstream: got %d, %v", n, err)
	}
	before, _ := c.Head()
	var out strings.Builder
	if err := c.Pull(nil, &out); err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	if after, err := c.Head(); err != nil || after == before || after != git(t, upstream, "rev-parse", "HEAD") {
		t.Errorf("head after the pull: %s, %v (was %s)", after, err, before)
	}
	var log strings.Builder
	if v, err := c.Rebuild(&log); err != nil || v != "opencode-sandbox 0.2.0 (opencode 1.0.0)" || log.String() != "built\n" {
		t.Errorf("rebuild: got %q, %v, log %q", v, err, log.String())
	}
}

// A pull never discards what was changed in the clone.
func TestClonePullKeepsLocalChanges(t *testing.T) {
	upstream, clone := repo(t)
	c := &Clone{Root: clone}
	write(t, filepath.Join(clone, "VERSION"), "mine\n", 0o644)
	release2(t, upstream)
	if err := c.Pull(nil, io.Discard); err == nil {
		t.Error("the pull went through over a local change")
	}
	if got, _ := os.ReadFile(filepath.Join(clone, "VERSION")); string(got) != "mine\n" {
		t.Errorf("the local change is gone: %q", got)
	}
}

func TestCloneAtAFixedCommit(t *testing.T) {
	_, clone := repo(t)
	git(t, clone, "checkout", "--quiet", "--detach")
	c := &Clone{Root: clone}
	if b := c.Branch(); b != "" {
		t.Errorf("branch: got %q, want none", b)
	}
	if _, err := c.Behind(); err == nil {
		t.Error("behind: no error without a branch")
	}
}
