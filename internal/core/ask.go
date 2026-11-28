package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"time"
)

// AskEnv set to "off" stops opencode-sandbox asking on screen about a host
// that's on no list of a sandbox with a filtered network: it's refused and
// written down, as it is where there's no screen to ask on.
const AskEnv = "OPENCODE_SANDBOX_ASK"

// What was answered when asked about a host.
type answer int

const (
	// answerNone: nobody was asked (no screen, too many questions), or
	// nobody answered in time. The host is refused, and may be asked about
	// again later.
	answerNone answer = iota
	answerDeny
	// answerOnce allows the host until the sandbox closes.
	answerOnce
	answerAlways
)

const (
	// askWait is how long a question stays on screen, and so how long a
	// connection waits for its answer.
	askWait = 30 * time.Second
	// askQueue is how many questions may wait their turn; a host asked
	// about beyond that is refused unasked.
	askQueue = 3
	// askQuiet is how long nothing is asked after a question went
	// unanswered: whoever might answer isn't there.
	askQuiet = 2 * time.Minute
)

// asker asks whoever is at this machine's screen whether a sandbox may
// reach a host that's on no list, one question at a time.
type asker struct {
	sandbox string
	// show puts the question on screen and waits for the answer.
	show func(ctx context.Context, sandbox, target string, wait time.Duration) answer
	// there says whether someone can answer now.
	there func() bool
	now   func() time.Time

	mu sync.Mutex
	// open are the questions asked or waiting to be, by host and port: a
	// host asked about by several connections is asked about once.
	open map[string]*question
	// quiet is until when nothing is asked.
	quiet time.Time
	turn  chan struct{} // held by the question on screen
}

type question struct {
	done chan struct{}
	ans  answer
}

// newAsker is an asker for this machine: one that asks on a Mac with
// someone logged in at its screen, and nowhere else. nil where nothing is
// ever asked.
func newAsker(sandbox string) *asker {
	switch strings.ToLower(os.Getenv(AskEnv)) {
	case "off", "0", "no", "false":
		return nil
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		return nil
	}
	return &asker{sandbox: sandbox, show: showDialog, there: atScreen, now: time.Now}
}

// about asks whether the sandbox may reach target (a host, with its port if
// it isn't a usual one), and waits for the answer, or until ctx is done.
func (a *asker) about(ctx context.Context, target string) answer {
	a.mu.Lock()
	if a.open == nil {
		a.open, a.turn = map[string]*question{}, make(chan struct{}, 1)
	}
	q := a.open[target]
	if q == nil {
		if len(a.open) > askQueue || a.now().Before(a.quiet) || !a.there() {
			a.mu.Unlock()
			return answerNone
		}
		q = &question{done: make(chan struct{})}
		a.open[target] = q
		go a.ask(target, q)
	}
	a.mu.Unlock()
	// A connection waits as long as one question may take, not for every
	// question before its own; the answer still counts for the next try.
	wait := time.NewTimer(askWait + 5*time.Second)
	defer wait.Stop()
	select {
	case <-q.done:
		return q.ans
	case <-ctx.Done():
	case <-wait.C:
	}
	return answerNone
}

// ask puts a question on screen when it's its turn.
func (a *asker) ask(target string, q *question) {
	a.turn <- struct{}{}
	ans := answerNone
	a.mu.Lock()
	quiet := a.now().Before(a.quiet)
	a.mu.Unlock()
	if !quiet { // the one before it may have gone unanswered
		ctx, cancel := context.WithTimeout(context.Background(), askWait+10*time.Second)
		ans = a.show(ctx, a.sandbox, target, askWait)
		cancel()
	}
	a.mu.Lock()
	if ans == answerNone && !quiet {
		a.quiet = a.now().Add(askQuiet)
	}
	q.ans = ans
	delete(a.open, target)
	a.mu.Unlock()
	close(q.done)
	<-a.turn
}

// atScreen says whether the person this runs as is logged in at this Mac's
// screen and the screen isn't locked.
func atScreen() bool {
	me, err := user.Current()
	if err != nil {
		return false
	}
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil || strings.TrimSpace(string(out)) != me.Username {
		return false
	}
	out, err = exec.Command("ioreg", "-n", "Root", "-d1").Output()
	return err == nil && !bytes.Contains(out, []byte(`"CGSSessionScreenIsLocked"=Yes`))
}

// dialog is the question, as an AppleScript. The sandbox's name, the host
// and the time to wait are its arguments: nothing of them is script.
const dialog = `on run argv
	set sandbox to item 1 of argv
	set target to item 2 of argv
	set secs to (item 3 of argv) as integer
	tell current application to activate
	set r to display dialog ("Sandbox '" & sandbox & "' wants to reach" & return & return & target & return & return & "That host is on no list of this sandbox's. What runs in the sandbox could send your files there.") with title "opencode-sandbox" buttons {"Always allow", "Allow until it closes", "Deny"} default button "Deny" cancel button "Deny" with icon caution giving up after secs
	if gave up of r then return "unanswered"
	return button returned of r
end run
`

// showDialog asks with a system dialog. Deny is its default: enter and
// escape both deny.
func showDialog(ctx context.Context, sandbox, target string, wait time.Duration) answer {
	cmd := exec.CommandContext(ctx, "osascript", "-", sandbox, target, fmt.Sprint(int(wait.Seconds())))
	cmd.Stdin = strings.NewReader(dialog)
	out, err := cmd.Output()
	return dialogAnswer(strings.TrimSpace(string(out)), err, ctx.Err())
}

// dialogAnswer reads what the dialog's script printed.
func dialogAnswer(out string, err, timedOut error) answer {
	switch {
	case timedOut != nil:
		return answerNone
	case out == "Always allow":
		return answerAlways
	case out == "Allow until it closes":
		return answerOnce
	case out == "Deny":
		return answerDeny
	case out == "unanswered":
		return answerNone
	}
	if ee, ok := err.(*exec.ExitError); ok && bytes.Contains(ee.Stderr, []byte("-128")) {
		return answerDeny // escape: the dialog was cancelled
	}
	return answerNone // no dialog could be shown
}
