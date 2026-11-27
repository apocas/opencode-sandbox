// Package run starts the external programs the launcher drives (docker,
// ssh-agent, ssh-add, ssh-keygen). Tests swap in a fake Runner.
package run

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Cmd is one program to run. Nil streams mean /dev/null.
type Cmd struct {
	Args   []string // Args[0] is the program
	Env    []string // added to the launcher's environment
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs a Cmd and returns its exit status. The error is only for a
// program that couldn't be started at all.
type Runner interface {
	Run(c *Cmd) (int, error)
}

// ExitError reports a program that failed after explaining why on stderr, so
// the launcher exits with the same status without adding a message (as bash
// does under `set -e`).
type ExitError struct {
	Code int
	Args []string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s exited with status %d", Quote(e.Args), e.Code)
}

// OS runs programs for real. With Debug set, each command is printed there
// first, so what the launcher does can be audited.
type OS struct{ Debug io.Writer }

func (o OS) Run(c *Cmd) (int, error) {
	if o.Debug != nil {
		env := ""
		if len(c.Env) > 0 {
			env = Quote(c.Env) + " "
		}
		fmt.Fprintf(o.Debug, "+ %s%s\n", env, Quote(c.Args))
	}
	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	return Status(cmd.Run())
}

// Status turns the error of a finished exec.Cmd into an exit status the way
// a shell reports it (128+N for a program killed by signal N).
func Status(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return -1, err
}

// Stderr receives what engines and helper programs print when they fail. It
// is the terminal's stderr, except where that would garble a full-screen
// interface, which captures it instead.
var Stderr io.Writer = os.Stderr

// Output runs args with stderr passed through and returns stdout without
// its trailing newlines, as $(...) does.
func Output(r Runner, args ...string) (string, error) {
	var out bytes.Buffer
	if err := Check(r, &Cmd{Args: args, Stdout: &out, Stderr: Stderr}); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Check runs c and turns a failure into an error: *ExitError when the
// program ran and failed.
func Check(r Runner, c *Cmd) error {
	code, err := r.Run(c)
	if err != nil {
		return fmt.Errorf("can't run %s: %w", c.Args[0], err)
	}
	if code != 0 {
		return &ExitError{Code: code, Args: c.Args}
	}
	return nil
}

// Quiet runs args with all output discarded and returns its exit status, or
// -1 when it couldn't be started.
func Quiet(r Runner, args ...string) int {
	code, err := r.Run(&Cmd{Args: args})
	if err != nil {
		return -1
	}
	return code
}

// Quote renders args as a shell would need them typed.
func Quote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
		}) < 0 {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}
