// Package tty has the few terminal helpers the launcher needs: whether a
// descriptor is a terminal, prompting for a line, and folding text.
package tty

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether fd refers to a terminal.
func IsTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlGetTermios, uintptr(unsafe.Pointer(&t)))
	return e == 0
}

// StdinIsTerminal is IsTerminal for standard input.
func StdinIsTerminal() bool { return IsTerminal(os.Stdin.Fd()) }

// ReadLine works like bash's `read -rp PROMPT var`: the prompt goes to stderr
// only when stdin is a terminal, surrounding spaces and tabs are trimmed, and
// a line that isn't ended by a newline counts as no answer. It reads a byte at
// a time so nothing meant for a later reader (the sandbox's terminal) is
// consumed.
func ReadLine(prompt string) (string, bool) {
	if StdinIsTerminal() {
		fmt.Fprint(os.Stderr, prompt)
	}
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				return strings.Trim(string(line), " \t"), true
			}
			line = append(line, b[0])
			continue
		}
		if err != nil {
			return strings.Trim(string(line), " \t"), false
		}
	}
}

// Fold breaks each line of text into pieces of at most width bytes at
// spaces, like `fold -s -w WIDTH`: a space stays at the end of the piece it
// ends, and a piece without spaces is cut at width.
func Fold(text string, width int) string {
	var out strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		for len(line) > width {
			cut := strings.LastIndexByte(line[:width], ' ') + 1
			if cut == 0 {
				cut = width
			}
			out.WriteString(line[:cut])
			out.WriteByte('\n')
			line = line[cut:]
		}
		out.WriteString(line)
	}
	return out.String()
}
