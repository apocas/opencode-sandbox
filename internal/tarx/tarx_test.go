package tarx

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, "w/sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "w/a.txt"), []byte("a"), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "w/.hidden"), []byte("h"), 0o600))
	must(t, os.WriteFile(filepath.Join(src, "w/sub/run.sh"), []byte("#!/bin/sh"), 0o4755))
	must(t, os.Symlink("a.txt", filepath.Join(src, "w/link")))

	for _, c := range []struct{ from, want string }{{"w", "w/"}, {"w/.", ""}, {"w/a.txt", ""}} {
		var b bytes.Buffer
		must(t, Write(&b, src+"/"+c.from)) // not Join: it would drop a final "/."
		dst := t.TempDir()
		must(t, Extract(&b, dst))
		switch c.from {
		case "w/a.txt":
			expect(t, dst, "a.txt", "a")
		default:
			expect(t, dst, c.want+"a.txt", "a")
			expect(t, dst, c.want+".hidden", "h")
			expect(t, dst, c.want+"sub/run.sh", "#!/bin/sh")
			if l, err := os.Readlink(filepath.Join(dst, c.want+"link")); err != nil || l != "a.txt" {
				t.Errorf("%s: link is %q, %v", c.from, l, err)
			}
			if fi, err := os.Stat(filepath.Join(dst, c.want+"sub/run.sh")); err != nil || fi.Mode().Perm() != 0o755 || fi.Mode()&os.ModeSetuid != 0 {
				t.Errorf("%s: run.sh mode wrong: %v", c.from, err)
			}
		}
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	cases := map[string][]tar.Header{
		"dot-dot":           {{Name: "../evil", Typeflag: tar.TypeReg}},
		"nested dot-dot":    {{Name: "a/../../evil", Typeflag: tar.TypeReg}},
		"absolute":          {{Name: filepath.Join(outside, "evil"), Typeflag: tar.TypeReg}},
		"through a symlink": {{Name: "l", Typeflag: tar.TypeSymlink, Linkname: outside}, {Name: "l/evil", Typeflag: tar.TypeReg}},
		"dir over symlink":  {{Name: "l", Typeflag: tar.TypeSymlink, Linkname: outside}, {Name: "l/", Typeflag: tar.TypeDir}, {Name: "l/evil", Typeflag: tar.TypeReg}},
		"hard link out":     {{Name: "h", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"}},
		"overwrite":         {{Name: "f", Typeflag: tar.TypeReg}, {Name: "f", Typeflag: tar.TypeReg}},
		"file over symlink": {{Name: "l", Typeflag: tar.TypeSymlink, Linkname: filepath.Join(outside, "evil")}, {Name: "l", Typeflag: tar.TypeReg}},
	}
	for name, hs := range cases {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		for _, h := range hs {
			h := h
			h.Mode = 0o644
			must(t, tw.WriteHeader(&h))
		}
		must(t, tw.Close())
		dst := t.TempDir()
		if err := Extract(&b, dst); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Lstat(filepath.Join(outside, "evil")); err == nil {
			t.Fatalf("%s: wrote outside the destination", name)
		}
	}
}

func TestExtractNeverOverwrites(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(src, "f"), []byte("new"), 0o644))
	must(t, os.WriteFile(filepath.Join(dst, "f"), []byte("old"), 0o644))
	var b bytes.Buffer
	must(t, Write(&b, filepath.Join(src, "f")))
	if Extract(&b, dst) == nil {
		t.Error("overwrote an existing file")
	}
	expect(t, dst, "f", "old")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != want {
		t.Errorf("%s: got %q, %v; want %q", name, got, err, want)
	}
}
