package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/state"
)

// capsEngine is an engine of which only what it can do matters.
type capsEngine struct {
	backend.Engine
	caps backend.Caps
}

func (e capsEngine) Caps() backend.Caps { return e.caps }

// notes collects what a Core reports.
type notes struct {
	Plain
	said []string
}

func (n *notes) Note(msg string) { n.said = append(n.said, msg) }
func (n *notes) Warn(msg string) { n.said = append(n.said, msg) }

const gatewayConfig = `{"model":"a/m","provider":{
	"a":{"npm":"@ai-sdk/openai-compatible","models":{"m":{}},"options":{"baseURL":"https://one.example/v1","apiKey":"SECRET-A","headers":{"X-Team":"SECRET-T"}}},
	"nowhere":{"models":{"n":{}},"options":{"apiKey":"SECRET-N"}},
	"keyless":{"models":{"k":{}},"options":{"baseURL":"http://localhost:11434/v1"}}}}`

// here points a test's runtime folder and opencode config at temporary ones.
func here(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("OPENCODE_SANDBOX_CONFIG", filepath.Join(dir, "opencode.json"))
	t.Setenv("HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(gatewayConfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteConfigKeepsSecretsHere(t *testing.T) {
	for name, caps := range map[string]backend.Caps{"docker": {FileBinds: true}, "apple": {}} {
		t.Run(name, func(t *testing.T) {
			here(t)
			rep := &notes{}
			a := &Core{Rep: rep}
			e := capsEngine{caps: caps}
			if !caps.FileBinds { // the sandbox's folder also gets the host keys it trusts
				_ = os.MkdirAll(state.DataDir(), 0o700)
				_ = os.WriteFile(state.KnownHosts(), nil, 0o644)
			}
			read := func() string {
				path := state.ConfigFile("demo")
				if !caps.FileBinds {
					path = filepath.Join(state.SandboxDir("demo"), "opencode.json")
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}

			if err := a.writeConfig(e, "demo", "a,nowhere,keyless", true); err != nil {
				t.Fatal(err)
			}
			cfg := read()
			var st gatewayState
			data, err := os.ReadFile(gatewayStateFile("demo"))
			if err != nil || json.Unmarshal(data, &st) != nil {
				t.Fatalf("the gateway's state: %v %s", err, data)
			}
			if len(st.Token) < 40 || strings.Join(st.Providers, ",") != "a,nowhere,keyless" {
				t.Errorf("state: %+v", st)
			}
			for _, secret := range []string{"SECRET-A", "SECRET-T", "one.example"} {
				if strings.Contains(cfg, secret) {
					t.Errorf("the sandbox's config has %s", secret)
				}
			}
			for _, want := range []string{`"baseURL": "http://127.0.0.1:17872/a"`, `"apiKey": "` + st.Token + `"`, `"X-Team": "` + st.Token + `"`,
				`"apiKey": "SECRET-N"`, `"baseURL": "http://localhost:11434/v1"`} {
				if !strings.Contains(cfg, want) {
					t.Errorf("the sandbox's config lacks %s:\n%s", want, cfg)
				}
			}
			// The token is the launcher's and the gateway's only.
			if fi, _ := os.Stat(gatewayStateFile("demo")); fi.Mode().Perm() != 0o600 {
				t.Errorf("the gateway's state is %v", fi.Mode().Perm())
			}
			if fi, _ := os.Stat(state.GatewayDir("demo")); fi.Mode().Perm() != 0o700 {
				t.Errorf("the gateway's folder is %v", fi.Mode().Perm())
			}
			if len(rep.said) != 1 || !strings.Contains(rep.said[0], "provider 'nowhere': its API key goes into sandbox 'demo'") {
				t.Errorf("said: %q", rep.said)
			}

			// Each start gets a token of its own.
			if err := a.writeConfig(e, "demo", "a", true); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(read(), st.Token) {
				t.Error("the token of the start before is still in the config")
			}

			// Without a gateway (a sandbox from before 0.9.0): as it always was.
			if err := a.writeConfig(e, "demo", "a", false); err != nil {
				t.Fatal(err)
			}
			if cfg := read(); !strings.Contains(cfg, "SECRET-A") || !strings.Contains(cfg, "https://one.example/v1") {
				t.Errorf("an old sandbox's config:\n%s", cfg)
			}
		})
	}
}

func TestKeysInside(t *testing.T) {
	here(t)
	a := &Core{}
	for _, c := range []struct {
		providers, gateway string
		want               bool
	}{
		{"a", "1", false},
		{"a,keyless", "1", false},
		{"a,nowhere", "1", true}, // the gateway doesn't know where nowhere's API is
		{"a", "", true},          // from before 0.9.0
		{"keyless", "", false},   // nothing to keep
		{"", "", true},           // from before provider selection: all of them
		{"gone", "1", false},     // no longer in your config
	} {
		if got := a.keysInside(backend.Row{Providers: c.providers, Gateway: c.gateway}); got != c.want {
			t.Errorf("providers %q, gateway %q: %v, want %v", c.providers, c.gateway, got, c.want)
		}
	}
	// A config that changes is read again.
	if err := os.WriteFile(state.Config(), []byte(`{"provider":{"a":{"options":{"apiKey":"k"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.keysInside(backend.Row{Providers: "a", Gateway: "1"}) {
		t.Error("the config changed (a has no baseURL now), but the old one was used")
	}
}
