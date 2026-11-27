package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// plain is frame f as text, on a terminal just large enough.
func plain(m *model, f int) string {
	return ansi.Strip(m.introView(f, introW, introH))
}

func TestIntroFrames(t *testing.T) {
	for i, line := range wordmark {
		if len(line) != markW {
			t.Errorf("row %d of the name is %d wide, want %d", i, len(line), markW)
		}
	}
	m := newTest(t)
	// It starts with nothing but the first stroke, and ends whole.
	first, last := plain(m, 0), plain(m, introFrames-1)
	if strings.ContainsAny(first, "╭╮╰╯") || strings.Contains(first, tagline) {
		t.Errorf("the first frame:\n%s", first)
	}
	for _, want := range []string{"╭" + strings.Repeat("─", wallW-2) + "╮", "╰" + strings.Repeat("─", wallW-2) + "╯",
		"│   " + string(wordmark[0]) + "   │", "│   " + string(wordmark[1]) + "   │", "│   " + string(wordmark[2]) + "   │", tagline} {
		if !strings.Contains(last, want) {
			t.Errorf("the last frame lacks %q:\n%s", want, last)
		}
	}
	// Every frame is the same size, fits the smallest terminal it's shown
	// on, and has no less in it than the one before: nothing flickers.
	ink := func(s string) int {
		return len([]rune(strings.Join(strings.Fields(strings.ReplaceAll(s, "▌", "")), "")))
	}
	prev := -1
	for f := 0; f < introFrames; f++ {
		frame := plain(m, f)
		lines := strings.Split(frame, "\n")
		if len(lines) != introH {
			t.Fatalf("frame %d has %d lines, want %d", f, len(lines), introH)
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w != introW {
				t.Fatalf("frame %d: a line %d wide, want %d: %q", f, w, introW, l)
			}
		}
		if n := ink(frame); n < prev {
			t.Errorf("frame %d has less in it (%d) than the one before (%d)", f, n, prev)
		} else {
			prev = n
		}
		// The wall isn't whole, nor the tagline shown, before the name is.
		whole := strings.Contains(frame, "╭"+strings.Repeat("─", wallW-2)+"╮")
		if f < sealFrame-2 && whole || f < sealFrame && strings.Contains(frame, tagline) || f >= sealFrame && !whole {
			t.Errorf("frame %d (the name is written at %d): wall whole %v\n%s", f, sealFrame, whole, frame)
		}
	}
	if d := introFrames * int(introTick.Milliseconds()); d < 900 || d > 1600 {
		t.Errorf("the intro takes %d ms", d)
	}
	if os.Getenv("SHOW") != "" {
		for _, f := range []int{0, 4, 9, 14, sealFrame, introFrames - 1} {
			t.Logf("frame %d\n%s", f, plain(m, f))
		}
	}
}

func TestIntroPlaysThenLeaves(t *testing.T) {
	m := newTest(t)
	m.intro = true
	out := screen(t, m, 100, 30, rowsMsg{ss: sample()})
	if !strings.Contains(out, "▌") || strings.Contains(out, "Sandboxes (") {
		t.Fatalf("the intro isn't what's shown first:\n%s", out)
	}
	// The dashboard has loaded behind it.
	if len(m.rows) != 3 {
		t.Errorf("%d sandboxes loaded during the intro", len(m.rows))
	}
	var cmd tea.Cmd
	for i := 0; i < introFrames-1; i++ {
		if _, cmd = m.Update(introMsg{}); !m.intro {
			t.Fatalf("the intro ended at frame %d of %d", i+1, introFrames)
		}
		if cmd == nil {
			t.Fatalf("no next frame after %d", i+1)
		}
	}
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, tagline) {
		t.Errorf("its last frame:\n%s", out)
	}
	if _, cmd = m.Update(introMsg{}); m.intro || cmd != nil {
		t.Errorf("the intro didn't end: playing %v, another frame %v", m.intro, cmd != nil)
	}
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "Sandboxes (3)") || strings.Contains(out, tagline) {
		t.Errorf("after the intro:\n%s", out)
	}
	// A frame that arrives late changes nothing.
	if _, cmd = m.Update(introMsg{}); m.intro || cmd != nil {
		t.Error("a late frame started it again")
	}
}

func TestIntroIsSkipped(t *testing.T) {
	// By the keys one skips things with, which then do nothing else: enter
	// doesn't open the first sandbox.
	for _, k := range []tea.KeyPressMsg{press("enter"), press("esc"), {Code: tea.KeySpace, Text: " "}} {
		m := newTest(t)
		m.intro = true
		screen(t, m, 100, 30, rowsMsg{ss: sample()})
		if _, cmd := m.Update(k); m.intro || cmd != nil || len(m.marked) != 0 || m.mode != listMode {
			t.Errorf("%s during the intro: playing %v, did something %v, marked %d, mode %v", k.String(), m.intro, cmd != nil, len(m.marked), m.mode)
		}
		if out := ansi.Strip(m.View().Content); !strings.Contains(out, "Sandboxes (3)") {
			t.Errorf("after skipping with %s:\n%s", k.String(), out)
		}
	}
	// By any other key, which also does its job.
	m := newTest(t)
	m.intro = true
	screen(t, m, 100, 30, rowsMsg{ss: sample()})
	if m.Update(press("?")); m.intro || !m.showHelp {
		t.Errorf("? during the intro: playing %v, help shown %v", m.intro, m.showHelp)
	}
	// On a terminal too small for it.
	for _, size := range [][2]int{{introW - 1, 30}, {100, introH - 1}} {
		m := newTest(t)
		m.intro = true
		if out := screen(t, m, size[0], size[1], rowsMsg{ss: sample()}); m.intro || strings.Contains(out, "▌") {
			t.Errorf("%dx%d: the intro played", size[0], size[1])
		}
	}
	// And where it's switched off.
	for v, want := range map[string]bool{"": true, "on": true, "off": false, "OFF": false, "0": false, "no": false, "false": false} {
		t.Setenv(IntroEnv, v)
		if introWanted() != want {
			t.Errorf("%s=%q: wanted %v", IntroEnv, v, !want)
		}
	}
	// Tests and anything else that makes a model get none unless they ask.
	if newTest(t).intro {
		t.Error("a new model plays the intro by itself")
	}
}
