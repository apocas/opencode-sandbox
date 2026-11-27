package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// harmless are the dot files and folders that hold no secrets by convention:
// what version control, editors, formatters and CI keep in a project. Any
// other dot file is named before its folder is copied into a sandbox.
var harmless = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`
		.git .gitignore .gitattributes .gitmodules .gitkeep .keep .mailmap .hgignore
		.github .gitlab .gitlab-ci.yml .circleci .travis.yml .gitpod.yml .devcontainer
		.editorconfig .vscode .idea .DS_Store
		.dockerignore .npmignore .prettierignore .eslintignore .stylelintignore
		.nvmrc .node-version .python-version .ruby-version .go-version .tool-versions
		.prettierrc .eslintrc .stylelintrc .babelrc .browserslistrc .swcrc .mocharc .nycrc
		.golangci.yml .golangci.yaml .goreleaser.yml .goreleaser.yaml
		.pre-commit-config.yaml .clang-format .clang-tidy .rubocop.yml .rspec .flake8 .pylintrc
		.shellcheckrc .yamllint .markdownlint.json .markdownlint.yaml .swiftlint.yml
		.husky .changeset .well-known`) {
		harmless[n] = true
	}
}

// isHarmless says whether a dot file's name is one that holds no secrets.
func isHarmless(name string) bool {
	if harmless[name] {
		return true
	}
	// A formatter's or linter's config in another format: .prettierrc.json.
	if i := strings.IndexByte(name[1:], '.'); i > 0 && harmless[name[:i+1]] && strings.HasSuffix(name[:i+1], "rc") {
		return true
	}
	// What a .env should look like, without its values.
	if strings.HasPrefix(name, ".env.") {
		for _, s := range []string{".example", ".sample", ".template", ".dist", ".defaults"} {
			if strings.HasSuffix(name, s) {
				return true
			}
		}
	}
	return false
}

// DotFiles are the dot files of a folder that may hold secrets.
type DotFiles struct {
	// Found are their paths in the folder; a folder's ends in /.
	Found []string
	// Partial: the folder is too large to look through in a moment, so
	// there may be more.
	Partial bool
	// File: what was looked at is a dot file itself, not a folder.
	File bool
}

// List names up to max of them: ".env, .npmrc and 3 more".
func (d DotFiles) List(max int) string {
	list := strings.Join(d.Found[:min(len(d.Found), max)], ", ")
	switch n := len(d.Found) - max; {
	case n > 0:
		list += fmt.Sprintf(" and %d more", n)
	case d.Partial:
		list += " and maybe more (the folder is large)"
	}
	return list
}

// Warning says that they go into a sandbox with the rest of what's copied
// from where; "" if there are none.
func (d DotFiles) Warning(where string) string {
	if len(d.Found) == 0 {
		return ""
	}
	if d.File {
		return where + " is a dot file, which often holds secrets: anything running in the sandbox can read it."
	}
	return fmt.Sprintf("%s has dot files, which often hold secrets, and they're copied into the sandbox with the rest: %s. Anything running in the sandbox can read them.",
		where, d.List(6))
}

// A password or a token in a git config: in a remote's address, or as a
// header git sends (what CI checkouts leave behind).
var gitSecret = regexp.MustCompile(`(?mi)^\s*url\s*=\s*\S+://[^/\s:@]+:[^/\s@]+@|^\s*extraheader\s*=.*authorization`)

// DotFiles looks through a folder (or at a file) that is about to be copied
// into a sandbox for the dot files that may hold secrets: all but the ones
// that don't by convention (see harmless). It doesn't look into dot folders
// or node_modules, and gives up on a folder too large to walk in a moment.
func (a *Core) DotFiles(path string) DotFiles {
	var d DotFiles
	const most = 200
	seen, until := 0, time.Now().Add(2*time.Second)
	if fi, err := os.Lstat(path); err == nil && !fi.IsDir() {
		if n := fi.Name(); strings.HasPrefix(n, ".") && !isHarmless(n) {
			d.Found, d.File = append(d.Found, n), true
		}
		return d
	}
	_ = filepath.WalkDir(path, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == path {
			return nil // what can't be read isn't copied either
		}
		if seen++; seen%512 == 0 && time.Now().After(until) || len(d.Found) >= most {
			d.Partial = true
			return filepath.SkipAll
		}
		name := e.Name()
		rel, _ := filepath.Rel(path, p)
		switch {
		case e.IsDir() && name == ".git":
			if data, err := os.ReadFile(filepath.Join(p, "config")); err == nil && gitSecret.Match(data) {
				d.Found = append(d.Found, rel+"/config (it has a password or a token)")
			}
			return filepath.SkipDir
		case e.IsDir() && name == "node_modules":
			return filepath.SkipDir
		case !strings.HasPrefix(name, "."):
			return nil
		case e.IsDir():
			if !isHarmless(name) {
				d.Found = append(d.Found, rel+"/")
			}
			return filepath.SkipDir
		case !isHarmless(name):
			d.Found = append(d.Found, rel)
		}
		return nil
	})
	return d
}
