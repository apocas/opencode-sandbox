package tty

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestFoldMatchesFold(t *testing.T) {
	if _, err := exec.LookPath("fold"); err != nil {
		t.Skip("fold not installed")
	}
	texts := []string{
		"(Keys from ~/.ssh aren't offered because on macOS, Docker runs containers in a VM that can't reach an ssh-agent on this machine: they would have to be copied into the sandbox. A new disposable key is stored inside it.)",
		"short",
		strings.Repeat("x", 100) + " tail",
		"exactly seventy-six characters wide, which must not be broken at all, ok!!",
	}
	for _, s := range texts {
		for _, w := range []int{76, 78, 20} {
			cmd := exec.Command("fold", "-s", "-w", strconv.Itoa(w))
			cmd.Stdin = strings.NewReader(s + "\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if got, want := Fold(s, w)+"\n", string(out); got != want {
				t.Errorf("width %d:\n got %q\nwant %q", w, got, want)
			}
		}
	}
}

// A descriptor left in non-blocking mode is put back; one that isn't stays.
func TestBlocking(t *testing.T) {
	// Raw descriptors: an os.File's Fd() would itself make them blocking.
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(p[0])
	defer syscall.Close(p[1])
	r, w := uintptr(p[0]), uintptr(p[1])
	nonblocking := func(fd uintptr) bool {
		flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if e != 0 {
			t.Fatal(e)
		}
		return flags&syscall.O_NONBLOCK != 0
	}
	if err := syscall.SetNonblock(int(w), true); err != nil {
		t.Fatal(err)
	}
	if !nonblocking(w) {
		t.Fatal("the pipe didn't go non-blocking")
	}
	Blocking(w, r)
	if nonblocking(w) || nonblocking(r) {
		t.Error("still non-blocking")
	}
	Blocking(w) // again: nothing to do
	if nonblocking(w) {
		t.Error("non-blocking after a second call")
	}
	Blocking(^uintptr(0)) // not a descriptor: no harm
}
