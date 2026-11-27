package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/core"
	"github.com/apocas/opencode-sandbox/internal/ui"
)

func newCLI() *cli {
	t := ui.NewTheme(true)
	x := &cli{t: t, c: core.New()}
	x.rep = ui.NewReporter(t)
	x.c.Rep = x.rep
	return x
}

func TestOldOptionsNameTheirReplacement(t *testing.T) {
	for old, want := range map[string]string{
		"--list": "opencode-sandbox ls", "--resume": "opencode-sandbox open", "--pubkey": "opencode-sandbox key",
		"--status": "opencode-sandbox agents", "--stop-agent": "opencode-sandbox agents stop", "-w": "opencode-sandbox new DIR",
		"-k": "opencode-sandbox new -k KEY", "--health": "opencode-sandbox health", "--rm": "opencode-sandbox rm",
	} {
		_, err := newCLI().main([]string{old})
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), old+" is now: "+want) {
			t.Errorf("%s: got %v", old, err)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	_, err := newCLI().main([]string{"frobnicate"})
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "unknown command: frobnicate") {
		t.Errorf("got %v", err)
	}
}

func TestHelp(t *testing.T) {
	var b bytes.Buffer
	newCLI().help(&b, "")
	for _, c := range commands {
		if !strings.Contains(b.String(), "  "+c.name) {
			t.Errorf("help doesn't list %s", c.name)
		}
	}
	b.Reset()
	newCLI().help(&b, "new")
	for _, want := range []string{"opencode-sandbox new [DIR]", "-k KEY", "-p PROVIDER", "-- ARGS"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("help new: missing %q", want)
		}
	}
}

func TestWantsHelp(t *testing.T) {
	if !wantsHelp([]string{"x", "-h"}) || wantsHelp([]string{"x", "--", "-h"}) || wantsHelp(nil) {
		t.Error("wantsHelp")
	}
}

func TestSetupArguments(t *testing.T) {
	_, err := newCLI().main([]string{"setup", "--now"})
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "setup takes only -y") {
		t.Errorf("got %v", err)
	}
}

func TestUpdateArguments(t *testing.T) {
	_, err := newCLI().main([]string{"update", "--force"})
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "update takes only --check") {
		t.Errorf("got %v", err)
	}
}
