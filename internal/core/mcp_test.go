package core

import (
	"os"
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/state"
)

const mcpConfig = `{"model":"a/m","provider":{
	"a":{"npm":"@ai-sdk/openai-compatible","models":{"m":{}},"options":{"baseURL":"https://one.example/v1","apiKey":"SECRET-A"}},
	"keyless":{"models":{"k":{}},"options":{"baseURL":"http://localhost:11434/v1"}}},
	"mcp":{
		"tracker":{"type":"remote","url":"https://tracker.example/mcp","headers":{"Authorization":"Bearer SECRET-M"}},
		"servers":{
			"search":{"type":"remote","url":"https://search.example/sse?key=SECRET-Q"},
			"docs":{"type":"remote","url":"https://docs.example/mcp"},
			"files":{"type":"local","command":["files-mcp"],"environment":{"FILES_TOKEN":"SECRET-F"}}}}}`

// The MCP servers of your config go into every sandbox; what a remote one
// has for a secret stays here, and what can't is said.
func TestWriteConfigKeepsMCPSecretsHere(t *testing.T) {
	here(t)
	if err := os.WriteFile(state.Config(), []byte(mcpConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := &notes{}
	a := &Core{Rep: rep}
	e := capsEngine{caps: backend.Caps{FileBinds: true}}
	read := func() string {
		data, err := os.ReadFile(state.ConfigFile("demo"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if err := a.writeConfig(e, "demo", "a", true, false); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	for _, secret := range []string{"SECRET-A", "SECRET-M", "SECRET-Q", "tracker.example", "search.example"} {
		if strings.Contains(cfg, secret) {
			t.Errorf("the sandbox's config has %s:\n%s", secret, cfg)
		}
	}
	for _, want := range []string{
		`"url": "http://127.0.0.1:17872/mcp/tracker"`, `"url": "http://127.0.0.1:17872/mcp/search"`,
		`"url": "https://docs.example/mcp"`, // nothing to keep: reached as it is
		`"FILES_TOKEN": "SECRET-F"`,         // it runs inside
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the sandbox's config lacks %s:\n%s", want, cfg)
		}
	}
	if len(rep.said) != 1 || !strings.Contains(rep.said[0], "MCP server 'files' has a secret that goes into sandbox 'demo': it runs inside the sandbox, with FILES_TOKEN in its environment") {
		t.Errorf("said: %q", rep.said)
	}

	// Without a network, a server with nothing to keep is served too.
	if err := a.writeConfig(e, "demo", "a", true, true); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `"url": "http://127.0.0.1:17872/mcp/docs"`) || strings.Contains(cfg, "docs.example") {
		t.Errorf("without a network:\n%s", cfg)
	}

	// Without a gateway, the config is yours as it is.
	if err := a.writeConfig(e, "demo", "a", false, false); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "Bearer SECRET-M") || !strings.Contains(cfg, "https://search.example/sse?key=SECRET-Q") {
		t.Errorf("without a gateway:\n%s", cfg)
	}

	// ls marks a sandbox with a secret inside: here, the local server's.
	for _, c := range []struct {
		providers, gateway string
		want               bool
	}{{"a", "1", true}, {"keyless", "1", true}, {"keyless", "", true}} {
		if got := a.keysInside(backend.Row{Providers: c.providers, Gateway: c.gateway}); got != c.want {
			t.Errorf("providers %q, gateway %q: %v, want %v", c.providers, c.gateway, got, c.want)
		}
	}
	// With only remote servers, a gateway keeps everything; without one, their secrets are inside.
	remote := `{"provider":{"keyless":{"models":{"k":{}},"options":{"baseURL":"http://localhost:11434/v1"}}},
		"mcp":{"tracker":{"type":"remote","url":"https://tracker.example/mcp","headers":{"Authorization":"Bearer SECRET-M"}}}}`
	if err := os.WriteFile(state.Config(), []byte(remote), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.keysInside(backend.Row{Providers: "keyless", Gateway: "1"}) {
		t.Error("a remote MCP server behind the gateway counts as a secret inside")
	}
	if !a.keysInside(backend.Row{Providers: "keyless"}) {
		t.Error("without a gateway, a remote MCP server's header isn't counted as inside")
	}
}
