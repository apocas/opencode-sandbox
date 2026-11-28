package gateway

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fr is a frame as gateway.pl sends it.
func fr(id uint32, typ byte, data string) []byte {
	n := len(data)
	b := []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id), typ, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	return append(b, data...)
}

// A connection nobody reads holds every other one up, but not for long: it's
// given up, and the others go on.
func TestSessionDropsAStalledConnection(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	var mu sync.Mutex
	s := newSession(pr, writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) }), 150*time.Millisecond)
	defer s.Close()
	defer pw.Close()
	go func() {
		_, _ = pw.Write(fr(1, frameOpen, ""))
		_, _ = pw.Write(fr(2, frameOpen, ""))
		// More than connection 1 may be ahead of its reader, who never reads.
		for i := 0; i < backlog+3; i++ {
			_, _ = pw.Write(fr(1, frameData, "stuck"))
		}
		_, _ = pw.Write(fr(2, frameData, "through"))
	}()
	var conns []net.Conn
	for range 2 {
		c, err := s.Accept()
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	started := time.Now()
	buf := make([]byte, 16)
	_ = conns[1].SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conns[1].Read(buf)
	if err != nil || string(buf[:n]) != "through" {
		t.Fatalf("the other connection got %q, %v", buf[:n], err)
	}
	if waited := time.Since(started); waited < 100*time.Millisecond || waited > 3*time.Second {
		t.Errorf("it waited %s for the stalled one", waited)
	}
	// The stalled one is closed here, and the sandbox is told.
	_ = conns[0].SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, conns[0]); err != nil && err != io.ErrClosedPipe {
		t.Errorf("the stalled connection ended with %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		told := bytes.Contains(out.Bytes(), fr(1, frameClose, ""))
		closed2 := bytes.Contains(out.Bytes(), fr(2, frameClose, ""))
		mu.Unlock()
		if told || time.Now().After(deadline) {
			if !told {
				t.Error("the sandbox wasn't told that the stalled connection is gone")
			}
			if closed2 {
				t.Error("the other connection was closed too")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// What the sandbox still sends for it goes nowhere, and ends nothing.
	_, _ = pw.Write(fr(1, frameData, "late"))
	_, _ = pw.Write(fr(2, frameData, "again"))
	_ = conns[1].SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conns[1].Read(buf); err != nil || string(buf[:n]) != "again" {
		t.Errorf("after that, the other connection got %q, %v", buf[:n], err)
	}
}
