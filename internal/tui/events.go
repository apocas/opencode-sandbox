package tui

import (
	"bytes"
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// events carries what core reports from background work to the dashboard.
type events chan tea.Msg

type (
	noteMsg struct {
		text string
		warn bool
	}
	stepMsg     struct{ title string }
	stepDoneMsg struct{}
	logMsg      struct{ line string }
)

// reporter is core's Reporter in the dashboard: everything becomes a
// message, nothing is written to the terminal.
type reporter struct {
	ch  events
	log *lines
}

func newReporter(ch events) reporter {
	return reporter{ch: ch, log: &lines{emit: func(l string) { ch <- logMsg{l} }}}
}

func (r reporter) Note(m string)  { r.ch <- noteMsg{text: m} }
func (r reporter) Warn(m string)  { r.ch <- noteMsg{text: m, warn: true} }
func (r reporter) Step(t string)  { r.ch <- stepMsg{t} }
func (r reporter) Done()          { r.ch <- stepDoneMsg{} }
func (r reporter) Log() io.Writer { return r.log }

// lines splits what's written to it into lines for emit.
type lines struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	emit func(string)
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		i := bytes.IndexAny(l.buf.Bytes(), "\r\n")
		if i < 0 {
			return len(p), nil
		}
		line := strings.TrimSpace(string(l.buf.Next(i + 1)))
		if line != "" {
			l.emit(line)
		}
	}
}

// listen waits for the next event.
func listen(ch events) tea.Cmd { return func() tea.Msg { return <-ch } }
