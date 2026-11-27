// Package gateway keeps your providers' API keys on this machine. A sandbox's
// opencode is pointed at a port inside the sandbox; what it sends there is
// relayed to this package over the engine's exec (see gateway.pl, the half
// that runs inside), which puts the real key in and passes the request on to
// the provider. Nothing listens on a network outside the sandbox.
package gateway

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// The frames exchanged with gateway.pl: a connection's number (4 bytes), a
// type (1 byte) and the length of the data (4 bytes), big-endian, then the
// data.
const (
	frameReady = 0 // the half inside is listening
	frameOpen  = 1 // a new connection there
	frameData  = 2
	frameClose = 3

	maxFrame = 32 << 10
	// maxStreams is the most connections a sandbox may have open at once;
	// more are closed as they come.
	maxStreams = 256
	// backlog is how many frames a connection may be ahead of its reader
	// before the whole channel waits for it.
	backlog = 64
)

// Session is this machine's end of the channel to a sandbox. It's a
// net.Listener: every connection made to the gateway's port inside the
// sandbox comes out of Accept. What arrives is the sandbox's word and no
// more: frames that make no sense end the session.
type Session struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	streams map[uint32]*stream

	conns chan net.Conn
	ready chan struct{}
	done  chan struct{}
	once  sync.Once
	err   error
}

// stream is one connection of the sandbox.
type stream struct {
	id uint32
	// in is what the sandbox sent, in order; closed when it closed.
	in chan []byte
	// near is our end of the pipe whose other end Accept returned.
	near net.Conn
	gone chan struct{}
	once sync.Once
}

func (st *stream) end() { st.once.Do(func() { close(st.gone); st.near.Close() }) }

// NewSession serves the channel: r is what the half inside writes, w what it
// reads.
func NewSession(r io.Reader, w io.Writer) *Session {
	s := &Session{w: w, streams: map[uint32]*stream{}, conns: make(chan net.Conn),
		ready: make(chan struct{}), done: make(chan struct{})}
	go func() { s.fail(s.read(r)) }()
	return s
}

// Ready is closed once the half inside the sandbox is listening.
func (s *Session) Ready() <-chan struct{} { return s.ready }

// Done is closed when the session is over; Err says why.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended: io.EOF if the half inside went away.
func (s *Session) Err() error {
	<-s.done
	return s.err
}

func (s *Session) fail(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		s.mu.Lock()
		for id, st := range s.streams {
			delete(s.streams, id)
			st.end()
		}
		s.mu.Unlock()
	})
}

// Accept returns the next connection made inside the sandbox.
func (s *Session) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

// Close ends the session and every connection of it.
func (s *Session) Close() error {
	s.fail(net.ErrClosed)
	return nil
}

type addr string

func (addr) Network() string  { return "opencode-sandbox" }
func (a addr) String() string { return string(a) }

// Addr is a placeholder: the listener is inside the sandbox.
func (s *Session) Addr() net.Addr { return addr("sandbox") }

// write sends one frame.
func (s *Session) write(id uint32, typ byte, data []byte) error {
	buf := make([]byte, 9+len(data))
	binary.BigEndian.PutUint32(buf, id)
	buf[4] = typ
	binary.BigEndian.PutUint32(buf[5:], uint32(len(data)))
	copy(buf[9:], data)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.w.Write(buf)
	return err
}

// read takes frames until the channel ends or one makes no sense.
func (s *Session) read(r io.Reader) error {
	var head [9]byte
	ready := false
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return err
		}
		id, typ, n := binary.BigEndian.Uint32(head[:]), head[4], binary.BigEndian.Uint32(head[5:])
		if n > maxFrame {
			return fmt.Errorf("a frame of %d bytes", n)
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		switch typ {
		case frameReady:
			if !ready {
				ready = true
				close(s.ready)
			}
		case frameOpen:
			if err := s.open(id); err != nil {
				return err
			}
		case frameData:
			s.mu.Lock()
			st := s.streams[id]
			s.mu.Unlock()
			if st == nil {
				continue // closed here already
			}
			select {
			case st.in <- data:
			case <-st.gone:
			case <-s.done:
				return nil
			}
		case frameClose:
			s.mu.Lock()
			st := s.streams[id]
			delete(s.streams, id)
			s.mu.Unlock()
			if st != nil {
				close(st.in) // what's left is delivered, then our end closes
			}
		default:
			return fmt.Errorf("a frame of type %d", typ)
		}
	}
}

// open starts a connection the sandbox made.
func (s *Session) open(id uint32) error {
	s.mu.Lock()
	if _, dup := s.streams[id]; dup {
		s.mu.Unlock()
		return fmt.Errorf("connection %d opened twice", id)
	}
	if len(s.streams) >= maxStreams {
		s.mu.Unlock()
		return s.write(id, frameClose, nil)
	}
	far, near := net.Pipe()
	st := &stream{id: id, in: make(chan []byte, backlog), near: near, gone: make(chan struct{})}
	s.streams[id] = st
	s.mu.Unlock()

	// What the sandbox sent goes to whoever accepted the connection.
	go func() {
		for {
			select {
			case data, ok := <-st.in:
				if !ok {
					st.end()
					return
				}
				if _, err := near.Write(data); err != nil {
					return
				}
			case <-st.gone:
				return
			}
		}
	}()
	// What they answer goes to the sandbox, until they close the connection.
	go func() {
		buf := make([]byte, maxFrame)
		for {
			n, err := near.Read(buf)
			if n > 0 {
				if werr := s.write(id, frameData, buf[:n]); werr != nil {
					s.fail(werr)
					return
				}
			}
			if err != nil {
				break
			}
		}
		s.mu.Lock()
		_, open := s.streams[id]
		delete(s.streams, id)
		s.mu.Unlock()
		st.end()
		if open { // the sandbox doesn't know yet
			if err := s.write(id, frameClose, nil); err != nil {
				s.fail(err)
			}
		}
	}()

	select {
	case s.conns <- far:
		return nil
	case <-s.done:
		far.Close()
		return errors.New("the session is over")
	}
}
