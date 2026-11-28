package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apocas/opencode-sandbox/internal/netpolicy"
)

// No test puts a question on the screen of whoever runs it.
func TestMain(m *testing.M) {
	os.Setenv(AskEnv, "off")
	os.Exit(m.Run())
}

// screen stands in for the dialog: it answers what it's told to, and keeps
// what it was asked.
type screen struct {
	mu      sync.Mutex
	asked   []string
	answers map[string]answer
	hold    chan struct{} // a question waits here before it's answered, if set
	away    bool
	clock   time.Time
}

func (s *screen) asker() *asker {
	s.clock = time.Unix(1_800_000_000, 0)
	return &asker{
		sandbox: "demo",
		show: func(_ context.Context, sandbox, target string, wait time.Duration) answer {
			s.mu.Lock()
			s.asked = append(s.asked, sandbox+" "+target+" "+wait.String())
			hold, ans := s.hold, s.answers[target]
			s.mu.Unlock()
			if hold != nil {
				<-hold
			}
			return ans
		},
		there: func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.away },
		now:   func() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.clock },
	}
}

func (s *screen) questions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func TestAsker(t *testing.T) {
	ctx := context.Background()
	s := &screen{answers: map[string]answer{"yes.example": answerAlways, "once.example": answerOnce, "no.example": answerDeny}}
	a := s.asker()
	for target, want := range map[string]answer{"yes.example": answerAlways, "once.example": answerOnce, "no.example": answerDeny} {
		if got := a.about(ctx, target); got != want {
			t.Errorf("%s: %v, want %v", target, got, want)
		}
	}
	if q := s.questions(); len(q) != 3 || !strings.HasPrefix(q[0], "demo ") || !strings.HasSuffix(q[0], " 30s") {
		t.Errorf("asked: %q", q)
	}

	// Several connections to one host are one question, and all get its answer.
	s = &screen{answers: map[string]answer{"busy.example": answerOnce}, hold: make(chan struct{})}
	a = s.asker()
	var wg sync.WaitGroup
	got := make([]answer, 5)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = a.about(ctx, "busy.example") }()
	}
	for len(s.questions()) == 0 {
		time.Sleep(time.Millisecond)
	}
	// While that one is on screen, three more hosts wait their turn; a fifth is refused unasked.
	waiting := make(chan answer, 3)
	for i := range 3 {
		go func() { waiting <- a.about(ctx, fmt.Sprintf("q%d.example", i)) }()
	}
	for {
		a.mu.Lock()
		n := len(a.open)
		a.mu.Unlock()
		if n == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ans := a.about(ctx, "one-too-many.example"); ans != answerNone {
		t.Errorf("a question beyond the queue: %v", ans)
	}
	// Whoever asked and gave up gets no answer; the question stays.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if ans := a.about(gone, "busy.example"); ans != answerNone {
		t.Errorf("for a connection that gave up: %v", ans)
	}
	close(s.hold)
	wg.Wait()
	for i, g := range got {
		if g != answerOnce {
			t.Errorf("connection %d got %v", i, g)
		}
	}
	// The three that waited were never answered (nobody pressed anything):
	// after the first of them, nothing more is put on screen for a while.
	for range 3 {
		if ans := <-waiting; ans != answerNone {
			t.Errorf("a queued question: %v", ans)
		}
	}
	if q := s.questions(); len(q) != 2 {
		t.Errorf("after one went unanswered, %d questions were shown: %q", len(q), q)
	}
	if ans := a.about(ctx, "later.example"); ans != answerNone || len(s.questions()) != 2 {
		t.Errorf("asked during the quiet time: %v, %q", ans, s.questions())
	}
	// Once the quiet time is over, questions are asked again.
	s.mu.Lock()
	s.clock = s.clock.Add(askQuiet + time.Second)
	s.answers["later.example"] = answerAlways
	s.mu.Unlock()
	if ans := a.about(ctx, "later.example"); ans != answerAlways {
		t.Errorf("after the quiet time: %v", ans)
	}
	// With nobody at the screen, nothing is asked.
	s.mu.Lock()
	s.away = true
	s.mu.Unlock()
	before := len(s.questions())
	if ans := a.about(ctx, "yes.example"); ans != answerNone || len(s.questions()) != before {
		t.Errorf("with nobody at the screen: %v", ans)
	}
}

func TestDialogAnswer(t *testing.T) {
	cancelled := &exec.ExitError{Stderr: []byte("execution error: User canceled. (-128)\n")}
	for _, c := range []struct {
		out      string
		err, ctx error
		want     answer
	}{
		{"Always allow", nil, nil, answerAlways},
		{"Allow until it closes", nil, nil, answerOnce},
		{"Deny", nil, nil, answerDeny},
		{"unanswered", nil, nil, answerNone},
		{"", cancelled, nil, answerDeny}, // escape
		{"", &exec.ExitError{Stderr: []byte("execution error: No user interaction allowed. (-1713)")}, nil, answerNone},
		{"", errors.New("no osascript"), nil, answerNone},
		{"Always allow", nil, context.DeadlineExceeded, answerNone},
		{"Something else", nil, nil, answerNone},
	} {
		if got := dialogAnswer(c.out, c.err, c.ctx); got != c.want {
			t.Errorf("%q, %v, %v: %v, want %v", c.out, c.err, c.ctx, got, c.want)
		}
	}
	// The question is the script's argument, never part of it.
	if strings.Contains(dialog, "%") || !strings.Contains(dialog, "item 2 of argv") || !strings.Contains(dialog, `default button "Deny" cancel button "Deny"`) {
		t.Error("the dialog's script")
	}
	// Asking is off here (see TestMain), as it is wherever it's switched off.
	if newAsker("demo") != nil {
		t.Error("an asker, with asking switched off")
	}
}

// What's answered on screen is what happens: a host is allowed for good,
// until the sandbox closes, or denied for good; unanswered, it's refused
// and may be asked about again.
func TestNetGuardAsks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	s := &screen{answers: map[string]answer{
		"always.example": answerAlways, "once.example": answerOnce, "no.example": answerDeny,
		"db.example:5432": answerOnce, "10.0.0.5": answerOnce,
	}}
	g := newNetGuard("demo", func(string, ...any) {})
	g.ask = s.asker()
	for _, c := range []struct {
		host string
		port int
		ok   bool
		why  string
	}{
		{"always.example", 443, true, ""},
		{"always.example", 22, true, ""}, // the rule it became covers the usual ports
		{"once.example", 443, true, ""},
		{"once.example", 80, true, ""},
		{"no.example", 443, false, "no.example is denied to sandbox 'demo' (rule no.example)"},
		{"no.example", 443, false, "is denied"},
		{"db.example", 5432, true, ""},
		{"once.example", 8443, false, "isn't on the list"}, // another port is another question, here unanswered
		{"silent.example", 443, false, "silent.example isn't on the list of hosts sandbox 'demo' may reach"},
	} {
		ok, why := g.decide(ctx, c.host, c.port)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%s:%d: %v %q, want %v %q", c.host, c.port, ok, why, c.ok, c.why)
		}
	}
	// Asked once each: what's answered isn't asked again.
	want := []string{"always.example", "once.example", "no.example", "db.example:5432", "once.example:8443"}
	q := s.questions()
	if len(q) != len(want) {
		t.Fatalf("asked %q, want about %q", q, want)
	}
	for i, w := range want {
		if q[i] != "demo "+w+" 30s" {
			t.Errorf("question %d: %q, want about %s", i, q[i], w)
		}
	}
	// (After the unanswered one, nothing is asked for a while: silent.example wasn't.)
	p, _ := netStore("demo").Load()
	if fmt.Sprint(p.Allow, p.Deny) != "[always.example] [no.example]" {
		t.Errorf("what was kept: %+v", p)
	}
	// What was allowed until the sandbox closes is in no file: the next
	// gateway of this sandbox knows nothing of it.
	again := newNetGuard("demo", func(string, ...any) {})
	if ok, _ := again.decide(ctx, "once.example", 443); ok {
		t.Error("allowed until it closes, and still allowed after")
	}
	if ok, _ := again.decide(ctx, "always.example", 443); !ok {
		t.Error("allowed for good, and not after")
	}
	// An address allowed on screen is an address named: this machine's too.
	s.mu.Lock()
	s.clock = s.clock.Add(askQuiet + time.Second)
	s.mu.Unlock()
	if ok, _ := g.decide(ctx, "10.0.0.5", 443); !ok || !g.allowsAddress(mustAddr("10.0.0.5"), 443) || g.allowsAddress(mustAddr("10.0.0.6"), 443) {
		t.Error("an address allowed until the sandbox closes")
	}
	// What opencode itself tries at every start isn't asked about: it's
	// refused and written down, to be allowed like any host.
	before := len(s.questions())
	if ok, why := g.decide(ctx, "models.opencode.ai", 443); ok || !strings.Contains(why, "allow demo models.opencode.ai") || len(s.questions()) != before {
		t.Errorf("opencode's own host: %v %q, %d questions", ok, why, len(s.questions())-before)
	}
	// Nor is anything asked about what couldn't be a rule.
	before = len(s.questions())
	if ok, _ := g.decide(ctx, "not_a-host-.example", 443); ok || len(s.questions()) != before {
		t.Errorf("what couldn't be a rule: %d questions", len(s.questions())-before)
	}
	// A denial in the rules is never asked about.
	_ = netStore("demo").Update(func(p *netpolicy.Policy) error { return p.DenyWord("*.blocked.example") })
	before = len(s.questions())
	if ok, _ := g.decide(ctx, "x.blocked.example", 443); ok || len(s.questions()) != before {
		t.Error("a denied host was asked about")
	}
}
