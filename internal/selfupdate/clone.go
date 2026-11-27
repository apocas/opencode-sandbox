package selfupdate

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Clone is a git clone of opencode-sandbox that builds the program it runs:
// the opencode-sandbox script at its top builds ROOT/.bin/opencode-sandbox
// whenever the sources changed.
type Clone struct{ Root string }

// CloneOf returns the clone that built exe, or nil if exe isn't one's.
func CloneOf(exe string) *Clone {
	bin := filepath.Dir(exe)
	if filepath.Base(bin) != ".bin" || filepath.Base(exe) != program {
		return nil
	}
	root := filepath.Dir(bin)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if line, _, _ := bytes.Cut(mod, []byte("\n")); err != nil || strings.TrimSpace(string(line)) != "module github.com/apocas/opencode-sandbox" {
		return nil
	}
	return &Clone{Root: root}
}

func (c *Clone) git(args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", c.Root}, args...)...)
}

// Branch is the branch the clone is on, or "" at a fixed commit (a tag, say).
func (c *Clone) Branch() string {
	out, err := c.git("symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Head is the commit the clone is at.
func (c *Clone) Head() (string, error) {
	out, err := c.git("rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out)), err
}

// Behind fetches, and returns how many commits the clone's branch lacks.
func (c *Clone) Behind() (int, error) {
	if out, err := c.git("fetch", "--quiet").CombinedOutput(); err != nil {
		return 0, fmt.Errorf("git fetch in %s: %s", c.Root, said(out, err))
	}
	cmd := c.git("rev-list", "--count", "HEAD..@{upstream}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git in %s: %s", c.Root, said(stderr.Bytes(), err))
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// Pull fast-forwards the clone's branch, on this terminal: git says what it
// did, and can ask for a passphrase. Local changes are never lost: where they
// are in the way, git refuses.
func (c *Clone) Pull(stdin io.Reader, out io.Writer) error {
	cmd := c.git("pull", "--ff-only")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, out, out
	return cmd.Run()
}

// Rebuild runs the clone's script, which builds the program if its sources
// changed, and returns what it says its version is. The build's own output
// goes to log.
func (c *Clone) Rebuild(log io.Writer) (string, error) {
	cmd := exec.Command(filepath.Join(c.Root, program), "version")
	cmd.Stderr = log
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line, nil
}

// said is what a failed command printed, or else the error itself.
func said(out []byte, err error) string {
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return err.Error()
}
