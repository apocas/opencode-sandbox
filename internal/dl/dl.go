// Package dl downloads what opencode-sandbox installs: its own releases, and
// what `setup` gets from Apple and gVisor.
package dl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Open starts downloading url; the caller closes the result. A download that
// starts over https is never continued over anything else.
func Open(ctx context.Context, c *http.Client, url, agent string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	client := *c
	secure := strings.HasPrefix(url, "https://")
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= 10:
			return errors.New("too many redirects")
		case secure && req.URL.Scheme != "https":
			return fmt.Errorf("refusing a redirect to %s://%s, which isn't https", req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// Text downloads a small text file, of at most max bytes.
func Text(ctx context.Context, c *http.Client, url, agent string, max int64) (string, error) {
	body, _, err := Open(ctx, c, url, agent)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, max))
	return string(data), err
}

// File downloads url into the new file dest, also writing what it reads to
// sum (a hash), and stops with an error past max bytes. progress, if not nil,
// is told how far it is: bytes so far, and the total or -1.
func File(ctx context.Context, c *http.Client, url, agent, dest string, max int64, sum io.Writer, progress func(done, total int64)) error {
	body, total, err := Open(ctx, c, url, agent)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w := io.Writer(f)
	if sum != nil {
		w = io.MultiWriter(f, sum)
	}
	if progress != nil {
		w = io.MultiWriter(w, &meter{total: total, tell: progress})
	}
	n, err := io.Copy(w, io.LimitReader(body, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("it's larger than %d MiB, which it shouldn't be", max>>20)
	}
	if err != nil {
		os.Remove(dest)
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	return nil
}

type meter struct {
	done, total int64
	tell        func(done, total int64)
}

func (m *meter) Write(p []byte) (int, error) {
	m.done += int64(len(p))
	m.tell(m.done, m.total)
	return len(p), nil
}
