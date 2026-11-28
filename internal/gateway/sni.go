package gateway

import (
	"encoding/binary"
	"errors"
)

// What the start of a connection is, as far as the server's name goes.
const (
	helloMore   = iota // too little so far to tell
	helloNotTLS        // not a TLS handshake: nothing to check
	helloNamed         // a TLS handshake; the name it asks for, or "" for none
	helloBad           // a TLS handshake that can't be read
)

// maxHello is the longest TLS hello that's read to its end.
const maxHello = 64 << 10

var errHello = errors.New("its TLS handshake can't be read")

// helloName reads the server's name (SNI) from the start of a connection:
// the first message of a TLS handshake, if that's what it is. A connection
// through the filter is to a host by name, and the name in the handshake is
// what a server that hosts many sites goes by.
func helloName(data []byte) (state int, name string) {
	// Records: type (22: handshake), version (3, x), length, payload. A hello
	// may be spread over several.
	var msg []byte
	rest := data
	for {
		if len(rest) == 0 {
			return helloMore, ""
		}
		if rest[0] != 22 {
			if len(msg) == 0 {
				return helloNotTLS, ""
			}
			return helloBad, ""
		}
		if len(rest) < 5 {
			return helloMore, ""
		}
		if rest[1] != 3 {
			if len(msg) == 0 {
				return helloNotTLS, ""
			}
			return helloBad, ""
		}
		n := int(binary.BigEndian.Uint16(rest[3:5]))
		if n == 0 || n > 16384+256 {
			return helloBad, ""
		}
		if len(rest) < 5+n {
			// The message's own length may already say it's too long.
			msg = append(msg, rest[5:]...)
			if len(msg) >= 4 && (msg[0] != 1 || msgLen(msg) > maxHello) {
				return helloBad, ""
			}
			return helloMore, ""
		}
		msg = append(msg, rest[5:5+n]...)
		rest = rest[5+n:]
		if len(msg) < 4 {
			continue
		}
		if msg[0] != 1 || msgLen(msg) > maxHello { // 1: ClientHello
			return helloBad, ""
		}
		if len(msg) >= 4+msgLen(msg) {
			name, err := sni(msg[4 : 4+msgLen(msg)])
			if err != nil {
				return helloBad, ""
			}
			return helloNamed, name
		}
	}
}

func msgLen(msg []byte) int { return int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3]) }

// sni finds the server's name in a ClientHello's body.
func sni(b []byte) (string, error) {
	skip := func(n int) error {
		if len(b) < n {
			return errHello
		}
		b = b[n:]
		return nil
	}
	// A vector: its length in n bytes, then that many.
	vec := func(n int) ([]byte, error) {
		if len(b) < n {
			return nil, errHello
		}
		l := 0
		for _, c := range b[:n] {
			l = l<<8 | int(c)
		}
		if len(b) < n+l {
			return nil, errHello
		}
		v := b[n : n+l]
		b = b[n+l:]
		return v, nil
	}
	if err := skip(2 + 32); err != nil { // version, random
		return "", err
	}
	for _, n := range []int{1, 2, 1} { // session id, cipher suites, compression
		if _, err := vec(n); err != nil {
			return "", err
		}
	}
	if len(b) == 0 {
		return "", nil // no extensions: no name
	}
	exts, err := vec(2)
	if err != nil || len(b) != 0 {
		return "", errHello
	}
	name, seen := "", false
	for len(exts) > 0 {
		if len(exts) < 4 {
			return "", errHello
		}
		typ, l := binary.BigEndian.Uint16(exts), int(binary.BigEndian.Uint16(exts[2:]))
		if len(exts) < 4+l {
			return "", errHello
		}
		ext := exts[4 : 4+l]
		exts = exts[4+l:]
		if typ != 0 { // 0: server_name
			continue
		}
		if seen {
			return "", errHello // twice: which one would the server go by?
		}
		seen = true
		// A list with one entry: type 0 (a host name), its length, the name.
		if len(ext) < 5 || int(binary.BigEndian.Uint16(ext)) != len(ext)-2 || ext[2] != 0 ||
			int(binary.BigEndian.Uint16(ext[3:])) != len(ext)-5 || len(ext) == 5 {
			return "", errHello
		}
		name = string(ext[5:])
	}
	return name, nil
}
