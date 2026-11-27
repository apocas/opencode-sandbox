package tty

import (
	"os/exec"
	"strconv"
	"strings"
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
