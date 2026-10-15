package proxy

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// clientHello is what interception needs from a TLS ClientHello.
type clientHello struct {
	sni  string
	alpn []string
}

var errNotTLS = errors.New("not TLS")

// peekClientHello looks at the first TLS record without consuming it.
func peekClientHello(br *bufio.Reader) (*clientHello, error) {
	hdr, err := br.Peek(5)
	if err != nil {
		return nil, err
	}
	if hdr[0] != 0x16 || hdr[1] != 3 {
		return nil, errNotTLS
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	rec, err := br.Peek(5 + n)
	if err != nil {
		return nil, err
	}
	return parseClientHello(rec[5:])
}

func parseClientHello(b []byte) (*clientHello, error) {
	bad := errors.New("ClientHello 格式错误")
	if len(b) < 4 || b[0] != 1 {
		return nil, bad
	}
	b = b[4:]
	if len(b) < 34 {
		return nil, bad
	}
	b = b[34:] // version, random
	skip := func(lenBytes int) bool {
		if len(b) < lenBytes {
			return false
		}
		var n int
		if lenBytes == 1 {
			n = int(b[0])
		} else {
			n = int(binary.BigEndian.Uint16(b))
		}
		if len(b) < lenBytes+n {
			return false
		}
		b = b[lenBytes+n:]
		return true
	}
	if !skip(1) || !skip(2) || !skip(1) {
		return nil, bad
	}
	h := &clientHello{}
	if len(b) < 2 {
		return h, nil
	}
	ext := b[2:]
	if n := int(binary.BigEndian.Uint16(b)); n < len(ext) {
		ext = ext[:n]
	}
	for len(ext) >= 4 {
		typ := binary.BigEndian.Uint16(ext)
		n := int(binary.BigEndian.Uint16(ext[2:]))
		if len(ext) < 4+n {
			break
		}
		data := ext[4 : 4+n]
		ext = ext[4+n:]
		switch typ {
		case 0: // server_name
			if len(data) < 2 {
				continue
			}
			list := data[2:]
			for len(list) >= 3 {
				l := int(binary.BigEndian.Uint16(list[1:]))
				if len(list) < 3+l {
					break
				}
				if list[0] == 0 {
					h.sni = string(list[3 : 3+l])
				}
				list = list[3+l:]
			}
		case 16: // ALPN
			if len(data) < 2 {
				continue
			}
			list := data[2:]
			for len(list) >= 1 {
				l := int(list[0])
				if len(list) < 1+l {
					break
				}
				h.alpn = append(h.alpn, string(list[1:1+l]))
				list = list[1+l:]
			}
		}
	}
	return h, nil
}

// peekedConn is a connection whose first bytes were read into a buffer.
type peekedConn struct {
	net.Conn
	r io.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// sniff waits briefly for the client's first bytes. ok is false when the
// client sent nothing in time: it may expect the server to speak first.
func sniff(c net.Conn, br *bufio.Reader, wait time.Duration) (first []byte, ok bool) {
	c.SetReadDeadline(time.Now().Add(wait))
	b, err := br.Peek(1)
	c.SetReadDeadline(time.Time{})
	if err != nil || len(b) == 0 {
		return nil, false
	}
	n := min(br.Buffered(), 8)
	b, _ = br.Peek(n)
	return b, true
}

// looksHTTP reports whether data starts like an HTTP/1 request.
func looksHTTP(b []byte) bool {
	for _, m := range []string{"GET ", "POST", "PUT ", "HEAD", "DELE", "OPTI", "PATC", "CONN", "TRAC", "PROP", "MKCO", "COPY", "MOVE", "LOCK", "UNLO", "SEAR", "REPO", "PRI "} {
		if len(b) >= 4 && string(b[:4]) == m {
			return true
		}
		if len(b) < 4 && len(b) > 0 && m[:len(b)] == string(b) {
			return true
		}
	}
	return false
}
