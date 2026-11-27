package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDotFiles(t *testing.T) {
	dir := t.TempDir()
	for path, content := range map[string]string{
		".env":                       "KEY=1",
		".env.local":                 "KEY=1",
		".env.example":               "KEY=",
		".npmrc":                     "//registry/:_authToken=x",
		".gitignore":                 "node_modules",
		".prettierrc.json":           "{}",
		".eslintrc.cjs":              "",
		".editorconfig":              "",
		".github/workflows/ci.yml":   "",
		".vscode/settings.json":      "{}",
		".aws/credentials":           "secret",
		".secrets/inner/.env":        "nested: the folder is named, not walked",
		"apps/web/.env.production":   "KEY=1",
		"apps/web/src/index.js":      "",
		"node_modules/pkg/.npmrc":    "someone else's",
		"README.md":                  "",
		".git/config":                "[remote \"origin\"]\n\turl = https://github.com/me/repo.git\n",
		"vendor/lib/.git/config":     "[remote \"origin\"]\n\turl = https://me:ghp_token@github.com/me/lib.git\n",
		"ci/.git/config":             "[http \"https://github.com/\"]\n\textraheader = AUTHORIZATION: basic abc\n",
		"apps/.git":                  "gitdir: ../.git/modules/apps",
		"docs/.envrc":                "export X=1",
		"docs/.env.sample":           "",
		"docs/.terraform/state.json": "",
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &Core{}
	d := a.DotFiles(dir)
	// (apps/.git is a file that points at a repository elsewhere: no secret.)
	want := []string{".aws/", ".env", ".env.local", ".npmrc", ".secrets/", "apps/web/.env.production",
		"ci/.git/config (it has a password or a token)", "docs/.envrc", "docs/.terraform/",
		"vendor/lib/.git/config (it has a password or a token)"}
	if got := strings.Join(d.Found, "|"); got != strings.Join(want, "|") || d.Partial || d.File {
		t.Errorf("found\n  %s\nwant\n  %s\n(partial %v, file %v)", strings.Join(d.Found, "\n  "), strings.Join(want, "\n  "), d.Partial, d.File)
	}
	if got := d.List(3); got != ".aws/, .env, .env.local and 7 more" {
		t.Errorf("List(3): %q", got)
	}
	if w := d.Warning("~/src/api"); w != ("~/src/api has dot files, which often hold secrets, and they're copied into the sandbox with the rest: .aws/, .env, .env.local, .npmrc, .secrets/, apps/web/.env.production and 4 more. Anything running in the sandbox can read them.") {
		t.Errorf("warning: %q", w)
	}

	// A folder without any: nothing to say.
	clean := filepath.Join(dir, "apps/web/src")
	if d := a.DotFiles(clean); len(d.Found) != 0 || d.Warning("x") != "" {
		t.Errorf("a folder without dot files: %+v", d)
	}
	// A file of its own.
	if d := a.DotFiles(filepath.Join(dir, ".env")); !d.File || d.Warning("/w/.env") != "/w/.env is a dot file, which often holds secrets: anything running in the sandbox can read it." {
		t.Errorf("a dot file: %+v %q", d, d.Warning("/w/.env"))
	}
	for _, harmlessFile := range []string{".gitignore", "README.md", ".env.example"} {
		if d := a.DotFiles(filepath.Join(dir, harmlessFile)); len(d.Found) != 0 {
			t.Errorf("%s: %+v", harmlessFile, d)
		}
	}
	// So many that the list stops: it says there may be more.
	many := t.TempDir()
	for i := 0; i < 250; i++ {
		_ = os.WriteFile(filepath.Join(many, ".secret"+string(rune('a'+i%26))+string(rune('a'+i/26))), nil, 0o644)
	}
	if d := a.DotFiles(many); !d.Partial || len(d.Found) != 200 || !strings.HasSuffix(d.List(300), "and maybe more (the folder is large)") {
		t.Errorf("many: partial %v, %d found, %q", d.Partial, len(d.Found), d.List(300)[len(d.List(300))-40:])
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); err == nil || len(a.DotFiles(filepath.Join(dir, "nope")).Found) != 0 {
		t.Error("a path that isn't there")
	}
}
