package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/gateway"
	"github.com/apocas/opencode-sandbox/internal/occonfig"
	"github.com/apocas/opencode-sandbox/internal/state"
)

const (
	// GatewayArg is the hidden command that serves an open sandbox's gateway
	// (see startGateway).
	GatewayArg = "--internal-gateway"
	// gatewayProgram is gateway.pl in the image.
	gatewayProgram = "/usr/local/bin/opencode-sandbox-gateway"
)

// gatewayWanted says whether new sandboxes get a gateway.
func gatewayWanted() bool { return gateway.Wanted(os.Getenv(gateway.Switch)) }

// config is your opencode config, read again when its file changes; nil if
// it can't be read.
func (a *Core) config() any {
	if s := stamp(state.Config()); s != a.cfgStamp {
		a.cfgRoot, _ = occonfig.Load(state.Config())
		a.cfgStamp = s
	}
	return a.cfgRoot
}

// keysInside says whether a sandbox has a secret of a provider or of an MCP
// server in its config, as your opencode config is now: all of them without
// a gateway, and with one, those the gateway can't serve or doesn't keep.
func (a *Core) keysInside(r backend.Row) bool {
	if r.Providers == "" {
		return true // from before provider selection: it has them all
	}
	routes, inside := gateway.Routes(a.config(), splitList(r.Providers), gatewayEnv(), false)
	servers, serversInside := gateway.MCP(a.config(), gatewayEnv(), false)
	if r.Gateway != "" {
		return len(inside)+len(serversInside) > 0
	}
	return len(routes)+len(inside)+len(servers)+len(serversInside) > 0
}

// gatewayState is what a sandbox's gateway is told by the launcher that
// wrote the sandbox's config.
type gatewayState struct {
	// Token stands for your API keys in the sandbox's config.
	Token string `json:"token"`
	// Providers are the sandbox's.
	Providers []string `json:"providers"`
	// All: the sandbox has no network, so the gateway serves each of them
	// whose API's address is known, not only those with secrets to keep.
	All bool `json:"all,omitempty"`
}

func gatewayStateFile(name string) string { return filepath.Join(state.GatewayDir(name), "state.json") }

// gatewayEnv is what your opencode config may refer to, on this machine.
func gatewayEnv() gateway.Env {
	return gateway.Env{Getenv: os.Getenv, Dir: filepath.Dir(state.Config()), Home: state.Home()}
}

// throughGateway changes a sandbox's config so that its providers' secrets
// stay here, and leaves the gateway what it needs to put them back. A new
// token every time: the one of an earlier start is worth nothing.
func (a *Core) throughGateway(name string, root any, cfg *occonfig.Object, providers []string, all bool) error {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	st := gatewayState{Token: "opencode-sandbox-" + hex.EncodeToString(b), Providers: providers, All: all}
	routes, inside := gateway.Routes(root, providers, gatewayEnv(), all)
	gateway.Rewrite(cfg, routes, st.Token)
	for _, id := range providers {
		if why, ok := inside[id]; ok {
			a.Rep.Warn(fmt.Sprintf("provider '%s' has a secret that goes into sandbox '%s': %s", id, name, why))
		}
	}
	// The MCP servers of your config go into every sandbox: theirs too.
	servers, inside := gateway.MCP(root, gatewayEnv(), all)
	gateway.RewriteMCP(cfg, servers, st.Token)
	for _, server := range sorted(inside) {
		a.Rep.Warn(fmt.Sprintf("MCP server '%s' has a secret that goes into sandbox '%s': %s", server, name, inside[server]))
	}
	if err := state.MkdirPrivate(state.RunDir()+"/gateways", state.GatewayDir(name)); err != nil {
		return err
	}
	data, _ := json.Marshal(st)
	out := gatewayStateFile(name)
	if err := os.WriteFile(out+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(out+".tmp", out)
}

// startGateway starts the gateway of a sandbox that's about to open, as a
// program of its own: it outlives this one where a sandbox is left running,
// and ends by itself when the sandbox closes.
func (a *Core) startGateway(name string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := state.GatewayDir(name)
	if err := state.MkdirPrivate(state.RunDir()+"/gateways", dir); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(dir + "/log"); err == nil && fi.Size() > 256<<10 {
		flags |= os.O_TRUNC
	}
	logFile, err := os.OpenFile(dir+"/log", flags, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(self, GatewayArg, name)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// lockGateway takes the lock of a sandbox's gateway, waiting a while for
// the one that served it until it closed a moment ago. nil: another gateway
// serves it.
func lockGateway(name string, wait time.Duration) *os.File {
	f, err := os.OpenFile(state.GatewayDir(name)+"/lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	for until := time.Now().Add(wait); ; time.Sleep(100 * time.Millisecond) {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return f
		}
		if time.Now().After(until) {
			f.Close()
			return nil
		}
	}
}

// GatewayRunning says whether a sandbox's gateway is being served.
func (a *Core) GatewayRunning(name string) bool {
	f, err := os.Open(state.GatewayDir(name) + "/lock")
	if err != nil {
		return false
	}
	defer f.Close()
	// Its lock is held for as long as it runs.
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Gateway serves a sandbox's gateway for as long as the sandbox is open.
// What it says goes to its log, in the sandbox's GatewayDir.
func (a *Core) Gateway(name string) {
	if !validName.MatchString(name) {
		return
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGINT) // the terminal's, not ours
	logger := log.New(os.Stderr, "", log.LstdFlags)
	lock := lockGateway(name, 10*time.Second)
	if lock == nil {
		return
	}
	defer lock.Close()
	a.findEngines()
	e := a.engineOf(name)
	if e == nil {
		logger.Printf("no sandbox named '%s'", name)
		return
	}
	c := state.Prefix + name
	// It was started just before its sandbox.
	for until := time.Now().Add(time.Minute); e.State(c) != "running"; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(until) {
			logger.Printf("sandbox '%s' didn't open", name)
			return
		}
	}
	src := &gatewaySource{name: name, logf: logger.Printf}
	proxy := &gateway.Proxy{Token: src.token, Routes: src.routes, Transport: gateway.DefaultTransport(), Log: logger.Printf}
	// With a filtered network, the gateway is also the sandbox's way out,
	// to the hosts on its list.
	var filter http.Handler
	if network, _ := e.Label(c, state.Label("network")); network == "filtered" {
		filter = newNetGuard(name, logger.Printf).filter()
	}
	handler := gateway.Handler(proxy, filter)
	logger.Printf("serving sandbox '%s'", name)
	for failures := 0; failures < 8; {
		started := time.Now()
		err := serveGateway(e, c, handler)
		if time.Since(started) > time.Minute {
			failures = 0
		}
		failures++
		// A sandbox that's closing takes a moment to say so.
		time.Sleep(time.Duration(failures) * 500 * time.Millisecond)
		if e.State(c) != "running" {
			logger.Printf("sandbox '%s' closed", name)
			return
		}
		logger.Printf("the channel into the sandbox ended (%v); starting it again", err)
	}
	logger.Printf("giving up: sandbox '%s' has no gateway until it's opened again", name)
}

// serveGateway runs the half of the gateway that's inside the sandbox and
// serves what it relays, until that half ends.
func serveGateway(e backend.Engine, c string, handler http.Handler) error {
	argv := e.ExecArgv(c, false, state.DetachKeys, gatewayProgram, "serve", strconv.Itoa(gateway.Port))
	cmd := exec.Command(argv[0], argv[1:]...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sess := gateway.NewSession(out, in)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute}
	go func() { _ = srv.Serve(sess) }()
	<-sess.Done()
	_ = srv.Close()
	_ = in.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return sess.Err()
}

// gatewaySource is what a gateway serves with: the token of the sandbox's
// config, and your providers as your opencode config has them now. Both are
// read again when their files change, so a rotated API key works at once.
type gatewaySource struct {
	name string
	logf func(string, ...any)

	mu       sync.Mutex
	st       gatewayState
	stStamp  string
	rt       gateway.Table
	cfgStamp string
}

// sorted lists a map's keys in order.
func sorted(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stamp changes when a file does.
func stamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size())
}

func (g *gatewaySource) load() {
	if s := stamp(gatewayStateFile(g.name)); s != g.stStamp {
		var st gatewayState
		data, err := os.ReadFile(gatewayStateFile(g.name))
		if err == nil {
			err = json.Unmarshal(data, &st)
		}
		if err != nil {
			g.logf("can't read the gateway's state: %v", err)
		}
		g.st, g.stStamp, g.cfgStamp = st, s, ""
	}
	if s := stamp(state.Config()); s != g.cfgStamp {
		root, err := occonfig.Load(state.Config())
		if err != nil {
			g.logf("can't read %s: %v", state.Config(), err)
		}
		routes, inside := gateway.Routes(root, g.st.Providers, gatewayEnv(), g.st.All)
		for id, why := range inside {
			g.logf("provider '%s' has a secret in the sandbox: %s", id, why)
		}
		servers, inside := gateway.MCP(root, gatewayEnv(), g.st.All)
		for name, why := range inside {
			g.logf("MCP server '%s' has a secret in the sandbox: %s", name, why)
		}
		g.rt, g.cfgStamp = gateway.Table{Providers: routes, MCP: servers}, s
	}
}

func (g *gatewaySource) token() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	return g.st.Token
}

func (g *gatewaySource) routes() gateway.Table {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	return g.rt
}
