package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
)

// Reporter shows what core is doing on the command line: a spinner for each
// long step, then ✓, and notes. Where stderr isn't a terminal, it writes
// plain "opencode-sandbox: …" lines instead.
type Reporter struct {
	t   *Theme
	w   io.Writer
	tty bool
	// Stream: long steps' output (image builds) goes straight to stderr
	// instead of being kept to show only if they fail.
	Stream bool

	mu      sync.Mutex
	step    string
	started time.Time
	stop    chan struct{}
	stopped chan struct{}
	log     tail
}

// NewReporter makes a Reporter writing to stderr.
func NewReporter(t *Theme) *Reporter {
	return &Reporter{t: t, w: os.Stderr, tty: IsTerminal(os.Stderr)}
}

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (r *Reporter) Step(title string) {
	r.finish(true)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.step, r.started = title, time.Now()
	r.log.reset()
	if !r.tty {
		fmt.Fprintf(r.w, "opencode-sandbox: %s…\n", title)
		return
	}
	if r.Stream {
		lipgloss.Fprintln(r.w, r.t.Accented.Render("›")+" "+title+"…")
		return
	}
	r.stop, r.stopped = make(chan struct{}), make(chan struct{})
	go r.spin(r.stop, r.stopped)
}

func (r *Reporter) spin(stop, stopped chan struct{}) {
	defer close(stopped)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		r.mu.Lock()
		el := ""
		if d := time.Since(r.started); d >= 3*time.Second {
			el = r.t.Dim.Render(fmt.Sprintf(" %s", d.Round(time.Second)))
		}
		lipgloss.Fprint(r.w, "\r\x1b[K"+r.t.Accented.Render(frames[i%len(frames)])+" "+r.step+"…"+el)
		r.mu.Unlock()
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

// finish ends the current step, if any, with ✓ or ✗.
func (r *Reporter) finish(ok bool) {
	r.mu.Lock()
	stop, stopped, step := r.stop, r.stopped, r.step
	r.stop, r.stopped, r.step = nil, nil, ""
	r.mu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}
	if step == "" || !r.tty {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop != nil {
		fmt.Fprint(r.w, "\r\x1b[K")
	}
	if ok {
		lipgloss.Fprintln(r.w, r.t.Success(step))
	} else {
		lipgloss.Fprintln(r.w, r.t.Failure(step))
	}
}

func (r *Reporter) Done() { r.finish(true) }

// Fail ends the current step with ✗ and shows the end of its output.
func (r *Reporter) Fail() {
	r.finish(false)
	if out := r.log.String(); out != "" && !r.Stream {
		r.mu.Lock()
		defer r.mu.Unlock()
		fmt.Fprint(r.w, out)
		if out[len(out)-1] != '\n' {
			fmt.Fprintln(r.w)
		}
	}
}

func (r *Reporter) line(styled, plain string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tty {
		fmt.Fprintln(r.w, "opencode-sandbox: "+plain)
		return
	}
	if r.stop != nil {
		fmt.Fprint(r.w, "\r\x1b[K") // the spinner redraws below
	}
	lipgloss.Fprintln(r.w, styled)
}

func (r *Reporter) Note(msg string) { r.line(r.t.Info(msg), msg) }
func (r *Reporter) Warn(msg string) { r.line(r.t.Warning(msg), msg) }

func (r *Reporter) Log() io.Writer {
	if r.Stream {
		return r.w
	}
	return &r.log
}

// tail keeps the last 64 KiB written to it.
type tail struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if n := t.buf.Len(); n > 64<<10 {
		t.buf.Next(n - 64<<10)
	}
	return len(p), nil
}

func (t *tail) String() string { t.mu.Lock(); defer t.mu.Unlock(); return t.buf.String() }
func (t *tail) reset()         { t.mu.Lock(); t.buf.Reset(); t.mu.Unlock() }
