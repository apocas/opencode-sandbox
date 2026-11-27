package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The intro, shown when the dashboard starts: the name is written while a
// wall rises around it and closes. A little over a second; any key skips it.

// IntroEnv set to "off" starts the dashboard without the intro.
const IntroEnv = "OPENCODE_SANDBOX_INTRO"

// wordmark is the name, three rows high.
var wordmark = [3][]rune{
	[]rune(`┌─┐┌─┐┌─┐┌┐┌┌─┐┌─┐┌┬┐┌─┐   ┌─┐┌─┐┌┐┌┌┬┐┌┐ ┌─┐─┐ ┬`),
	[]rune(`│ │├─┘├┤ ││││  │ │ ││├┤ ───└─┐├─┤│││ ││├┴┐│ │┌┴┬┘`),
	[]rune(`└─┘┴  └─┘┘└┘└─┘└─┘─┴┘└─┘   └─┘┴ ┴┘└┘─┴┘└─┘└─┘┴ └─`),
}

const tagline = "a copy of your folder · a key of its own · no API keys"

const (
	markW  = 49 // the wordmark's width
	nameW  = 24 // of which "opencode", before the dash
	dashW  = 3
	wallW  = markW + 8 // the wall around it, with three blanks either side
	wallH  = 7
	introW = wallW + 2 // the least terminal it's shown on
	introH = wallH + 4

	introTick  = 40 * time.Millisecond
	writeSpeed = 3 // columns of the name a frame
	wallSpeed  = 4 // cells of each half of the wall a frame
	// sealFrame is when the name is written; the wall has closed by then.
	sealFrame   = (markW + writeSpeed - 1) / writeSpeed
	_           = uint(sealFrame*wallSpeed - (2*(wallW-1-wallW/2) + wallH))
	flashFrames = 3              // the wall lights up as it closes
	introFrames = sealFrame + 18 // and the whole is held for a moment
)

// What a cell of the intro is, for its colour.
const (
	cBlank = iota
	cName
	cDash
	cBox
	cWall
	cSealed
	cCursor
)

type introMsg struct{}

func introNext() tea.Cmd {
	return tea.Tick(introTick, func(time.Time) tea.Msg { return introMsg{} })
}

// introWanted says whether the dashboard starts with the intro.
func introWanted() bool {
	switch strings.ToLower(os.Getenv(IntroEnv)) {
	case "off", "0", "no", "false":
		return false
	}
	return true
}

// wallPath is one half of the wall, from the middle of its foot up one side
// to the middle of its top: the cells in the order they're laid.
func wallPath(right bool) [][2]int {
	mid, edge, step := wallW/2, wallW-1, 1
	if !right {
		mid, edge, step = wallW/2-1, 0, -1
	}
	var p [][2]int
	for x := mid; x != edge; x += step {
		p = append(p, [2]int{x, wallH - 1})
	}
	for y := wallH - 1; y >= 0; y-- {
		p = append(p, [2]int{edge, y})
	}
	for x := edge - step; x != mid-step; x -= step {
		p = append(p, [2]int{x, 0})
	}
	return p
}

// wallRune is the piece of wall at a cell.
func wallRune(x, y int) rune {
	switch {
	case x == 0 && y == 0:
		return '╭'
	case x == wallW-1 && y == 0:
		return '╮'
	case x == 0 && y == wallH-1:
		return '╰'
	case x == wallW-1 && y == wallH-1:
		return '╯'
	case y == 0 || y == wallH-1:
		return '─'
	}
	return '│'
}

// introCells is frame f of the intro: what's in each cell, and what kind of
// thing it is.
func introCells(f int) (runes [wallH][wallW]rune, kinds [wallH][wallW]int) {
	for y := range runes {
		for x := range runes[y] {
			runes[y][x] = ' '
		}
	}
	// The name, written from the left.
	written := min(f*writeSpeed, markW)
	for row, line := range wordmark {
		for x := 0; x < written; x++ {
			k := cBox
			switch {
			case x < nameW:
				k = cName
			case x < nameW+dashW:
				k = cDash
			}
			if line[x] != ' ' {
				runes[2+row][4+x], kinds[2+row][4+x] = line[x], k
			}
		}
		if written < markW {
			runes[2+row][4+written], kinds[2+row][4+written] = '▌', cCursor
		}
	}
	// The wall, rising from the middle of its foot on both sides.
	k := cWall
	if f >= sealFrame && f < sealFrame+flashFrames {
		k = cSealed
	}
	for _, right := range []bool{false, true} {
		p := wallPath(right)
		for _, c := range p[:min(f*wallSpeed, len(p))] {
			runes[c[1]][c[0]], kinds[c[1]][c[0]] = wallRune(c[0], c[1]), k
		}
	}
	return
}

// introView draws frame f in the middle of a w×h terminal.
func (m *model) introView(f, w, h int) string {
	t := m.t
	// Colour only: bold box-drawing characters come from another font in
	// some terminals, and no longer line up.
	styles := map[int]lipgloss.Style{
		cDash: t.Dim, cBox: t.Accented, cWall: t.Accented, cCursor: t.Fg(t.OK), cSealed: t.Fg(t.OK),
	}
	runes, kinds := introCells(f)
	var b strings.Builder
	for y := range runes {
		// One run of the same kind at a time.
		for x := 0; x < wallW; {
			end := x
			for end < wallW && kinds[y][end] == kinds[y][x] {
				end++
			}
			s := string(runes[y][x:end])
			if st, ok := styles[kinds[y][x]]; ok {
				s = st.Render(s)
			}
			b.WriteString(s)
			x = end
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if f >= sealFrame {
		pad := (wallW - lipgloss.Width(tagline)) / 2
		b.WriteString(strings.Repeat(" ", pad) + t.Dim.Render(tagline))
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, b.String())
}
