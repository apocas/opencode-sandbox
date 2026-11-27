// Package tarx moves files in and out of containers as tar streams, for
// engines whose own copy command can't be used. What comes out of a sandbox
// was written by the agent, so Extract trusts nothing in the stream.
package tarx

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Write writes src as a tar stream: a file, or a folder with everything in
// it, under its own name; or, for "DIR/.", what's in DIR. Symlinks are
// stored as links; sockets, devices and pipes are left out.
func Write(w io.Writer, src string) error {
	tw := tar.NewWriter(w)
	root, prefix := src, filepath.Base(src)
	if filepath.Base(src) == "." {
		root, prefix = filepath.Dir(src), ""
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		if name == "." || name == "" {
			return nil // the folder whose contents are copied
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !fi.Mode().IsRegular() && !fi.IsDir():
			return nil
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = name, 0, 0, "", ""
		h.Format = tar.FormatPAX
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Extract unpacks a tar stream into the existing folder dest. It never
// overwrites anything, never writes outside dest or through a symlink, and
// drops setuid, setgid and sticky bits. Devices and pipes are skipped.
func Extract(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := clean(h.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err := parents(dest, name); err != nil {
			return err
		}
		mode := fs.FileMode(h.Mode) & fs.ModePerm
		switch h.Typeflag {
		case tar.TypeDir:
			if fi, err := os.Lstat(target); err == nil {
				if !fi.IsDir() {
					return fmt.Errorf("%s is in the way", name)
				}
				continue
			}
			if err := os.Mkdir(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, mode); err != nil {
				return err
			}
			_ = os.Chtimes(target, h.ModTime, h.ModTime)
		case tar.TypeSymlink:
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			// a second name for a file already extracted: copy it
			other, err := clean(h.Linkname)
			if err != nil || other == "" {
				return fmt.Errorf("bad hard link %s -> %s", h.Name, h.Linkname)
			}
			if err := parents(dest, other); err != nil {
				return err
			}
			src := filepath.Join(dest, filepath.FromSlash(other))
			fi, err := os.Lstat(src)
			if err != nil || !fi.Mode().IsRegular() {
				return fmt.Errorf("bad hard link %s -> %s", h.Name, h.Linkname)
			}
			f, err := os.Open(src)
			if err != nil {
				return err
			}
			err = writeFile(target, f, fi.Mode().Perm())
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}

// clean checks a name from a tar stream and makes it relative: "" for the
// top folder itself.
func clean(name string) (string, error) {
	n := path.Clean(strings.TrimPrefix(name, "./"))
	if path.IsAbs(name) || n == ".." || strings.HasPrefix(n, "../") {
		return "", fmt.Errorf("refusing %q: it points outside the destination", name)
	}
	if n == "." {
		return "", nil
	}
	return n, nil
}

// parents makes sure every folder above name in dest is a real folder (not
// a symlink), creating missing ones.
func parents(dest, name string) error {
	dir := dest
	parts := strings.Split(name, "/")
	for _, p := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, p)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("refusing %q: %s isn't a folder", name, p)
		}
	}
	return nil
}

func writeFile(target string, r io.Reader, mode fs.FileMode) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode&0o777|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode&0o777|0o600)
}
