package occonfig

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// bashFilter is the jq program of the bash launcher (0.4.0), which Filter
// must match exactly.
const bashFilter = `
  def ok: (split("/")[0]) as $p | $keep | index($p) != null;
  def prune: if (.model? | type) == "string" and (.model | ok | not) then del(.model) else . end;
  .provider = ((.provider // {}) | with_entries(select(.key as $k | $keep | index($k) != null)))
  | .enabled_providers = $keep
  | prune
  | if (.small_model? | type) == "string" and (.small_model | ok | not) then del(.small_model) else . end
  | if (.agent? | type) == "object" then .agent |= map_values(if type == "object" then prune else . end) else . end
  | if (.mode? | type) == "object" then .mode |= map_values(if type == "object" then prune else . end) else . end
  | if has("model") then . else
      (.provider[$keep[0]].models // {} | keys_unsorted[0]) as $m
      | if $m then .model = "\($keep[0])/\($m)" else . end
    end`

var fixtures = map[string]string{
	"single": `{"$schema":"https://opencode.ai/config.json","model":"p1/m1","provider":{"p1":{"name":"One","npm":"@ai-sdk/openai-compatible","models":{"m1":{},"m2":{}},"options":{"apiKey":"k1","baseURL":"https://one.example/v1"}}},"small_model":"p1/m2"}`,
	"several": `{"provider":{"zeta":{"models":{"z2":{},"z1":{}}},"alpha":{"name":"Alpha","models":{"a1":{}}},"mid":{}},"model":"alpha/a1","small_model":"zeta/z1",
		"agent":{"build":{"model":"zeta/z2","temperature":0.1},"plan":{"model":"alpha/a1"},"x":"string"},
		"mode":{"fast":{"model":"mid/q"}},"mcp":{"srv":{"type":"remote","url":"https://mcp.example","headers":{"Authorization":"Bearer secret"}}},
		"enabled_providers":["old"],"big":12345678901234567890,"float":1.50,"esc":"<a>&\u2028"}`,
	"no model":      `{"provider":{"b":{"models":{"bm":{}}},"a":{"models":{}}}}`,
	"null provider": `{"provider":null,"model":"x/y"}`,
}

func TestFilterMatchesJq(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	keeps := [][]string{{"p1"}, {"zeta"}, {"alpha", "zeta"}, {"mid"}, {"a"}, {"b"}, {"nope"}}
	for name, src := range fixtures {
		root, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, keep := range keeps {
			got, err := Filter(root, keep)
			if err != nil {
				t.Fatalf("%s %v: %v", name, keep, err)
			}
			keepJSON := `["` + strings.Join(keep, `","`) + `"]`
			cmd := exec.Command("jq", "--argjson", "keep", keepJSON, bashFilter)
			cmd.Stdin = strings.NewReader(src)
			want, err := cmd.Output()
			if err != nil {
				t.Fatalf("jq %s %v: %v", name, keep, err)
			}
			// Compare in one rendering, so only content and key order count.
			wantRoot, err := Parse(want)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := Encode(got), Encode(wantRoot); !bytes.Equal(g, w) {
				t.Errorf("%s keep=%v:\n got: %s\nwant: %s", name, keep, g, w)
			}
		}
	}
}

func TestProviderIDsAndMenu(t *testing.T) {
	root, _ := Parse([]byte(fixtures["several"]))
	if got := strings.Join(ProviderIDs(root), ","); got != "zeta,alpha,mid" {
		t.Errorf("ids in order: got %s", got)
	}
	var got []string
	for _, p := range Menu(root) {
		got = append(got, p.ID+"="+p.Description)
	}
	want := "zeta= (2 models)|alpha=Alpha (1 model)|mid= (0 models)"
	if strings.Join(got, "|") != want {
		t.Errorf("menu: got %s, want %s", strings.Join(got, "|"), want)
	}
}

func TestParseKeepsNumbersAndRejectsTrailingData(t *testing.T) {
	root, err := Parse([]byte(`{"n": 1.50, "big": 12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Encode(root)); got != "{\n  \"n\": 1.50,\n  \"big\": 12345678901234567890\n}\n" {
		t.Errorf("got %q", got)
	}
	if _, err := Parse([]byte(`{} {}`)); err == nil {
		t.Error("two values accepted")
	}
}
