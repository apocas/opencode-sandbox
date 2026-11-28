package tui

import (
	"io"
	"os/exec"

	"github.com/apocas/opencode-sandbox/internal/tty"
)

// shellCommand runs a program on the dashboard's terminal (a shell in a
// sandbox) and puts the terminal back in blocking mode when it ends: an
// engine's client may leave it otherwise, and the dashboard drawn next would
// come out in pieces.
type shellCommand struct{ cmd *exec.Cmd }

func (s *shellCommand) SetStdin(r io.Reader)  { s.cmd.Stdin = r }
func (s *shellCommand) SetStdout(w io.Writer) { s.cmd.Stdout = w }
func (s *shellCommand) SetStderr(w io.Writer) { s.cmd.Stderr = w }

func (s *shellCommand) Run() error {
	defer tty.BlockingStdio()
	return s.cmd.Run()
}
