package proxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"nlrcapture/internal/capture"
)

const (
	maxWSCapture  = 256 << 10 // bytes kept per message
	maxWSMessages = 20000
)

// relayUpgraded relays an upgraded connection in both directions. For
// WebSocket the frames are parsed on the way and the messages recorded.
func (e *Engine) relayUpgraded(r *rec, client net.Conn, cbr *bufio.Reader, up *upConn, respHdr capture.Header, ws bool) {
	var deflate, clientNoCtx, serverNoCtx bool
	if ws {
		ext := strings.ToLower(respHdr.Get("Sec-WebSocket-Extensions"))
		deflate = strings.Contains(ext, "permessage-deflate")
		clientNoCtx = strings.Contains(ext, "client_no_context_takeover")
		serverNoCtx = strings.Contains(ext, "server_no_context_takeover")
	}
	var mu sync.Mutex
	add := func(m capture.WSMessage) {
		mu.Lock()
		defer mu.Unlock()
		r.update(func(s *capture.Session) {
			if len(s.WS) < maxWSMessages {
				s.WS = append(s.WS, m)
			}
		})
	}
	done := make(chan error, 2)
	go func() {
		var err error
		if ws {
			p := &wsParser{fromClient: true, deflate: deflate, noCtx: clientNoCtx, emit: add}
			err = p.relay(up.Conn, cbr)
		} else {
			_, err = io.Copy(up.Conn, cbr)
		}
		if cw, ok := up.Conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- err
	}()
	go func() {
		var err error
		if ws {
			p := &wsParser{fromClient: false, deflate: deflate, noCtx: serverNoCtx, emit: add}
			err = p.relay(client, up.br)
		} else {
			_, err = io.Copy(client, up.br)
		}
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- err
	}()
	<-done
	// Give the other direction a moment to deliver a closing frame.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		client.Close()
		up.Close()
		<-done
	}
	r.update(func(s *capture.Session) {
		s.Ended = time.Now()
		if s.State != capture.StateFailed {
			s.State = capture.StateDone
		}
	})
}

// wsParser copies WebSocket frames from src to dst unchanged and reports
// each complete message.
type wsParser struct {
	fromClient bool
	deflate    bool
	noCtx      bool
	emit       func(capture.WSMessage)

	// the message being reassembled from fragments
	opcode     byte
	compressed bool
	buf        []byte
	length     int
	// window is the tail of what was inflated so far, the dictionary for
	// the next message when the context is taken over.
	window []byte
	fr     io.ReadCloser
}

func (p *wsParser) relay(dst io.Writer, src *bufio.Reader) error {
	hdr := make([]byte, 14)
	payload := make([]byte, 32<<10)
	for {
		if _, err := io.ReadFull(src, hdr[:2]); err != nil {
			return err
		}
		n := 2
		fin := hdr[0]&0x80 != 0
		rsv1 := hdr[0]&0x40 != 0
		op := hdr[0] & 0x0f
		masked := hdr[1]&0x80 != 0
		length := uint64(hdr[1] & 0x7f)
		switch length {
		case 126:
			if _, err := io.ReadFull(src, hdr[2:4]); err != nil {
				return err
			}
			length = uint64(binary.BigEndian.Uint16(hdr[2:4]))
			n = 4
		case 127:
			if _, err := io.ReadFull(src, hdr[2:10]); err != nil {
				return err
			}
			length = binary.BigEndian.Uint64(hdr[2:10])
			n = 10
		}
		var mask []byte
		if masked {
			if _, err := io.ReadFull(src, hdr[n:n+4]); err != nil {
				return err
			}
			mask = append([]byte(nil), hdr[n:n+4]...)
			n += 4
		}
		if _, err := dst.Write(hdr[:n]); err != nil {
			return err
		}
		if op != 0 && op < 8 {
			p.opcode, p.compressed, p.buf, p.length = op, rsv1, p.buf[:0], 0
		}
		control := op >= 8
		var cbuf []byte
		left := length
		var off uint64
		for left > 0 {
			k := min(left, uint64(len(payload)))
			if _, err := io.ReadFull(src, payload[:k]); err != nil {
				return err
			}
			if _, err := dst.Write(payload[:k]); err != nil {
				return err
			}
			data := payload[:k]
			if masked {
				data = append([]byte(nil), data...)
				for i := range data {
					data[i] ^= mask[(off+uint64(i))&3]
				}
			}
			if control {
				cbuf = append(cbuf, data...)
			} else if p.compressed || len(p.buf) < maxWSCapture {
				// Compressed messages are kept whole: they can only be
				// read once inflated.
				if p.compressed && len(p.buf) < 16<<20 || len(p.buf) < maxWSCapture {
					p.buf = append(p.buf, data...)
				}
			}
			if !control {
				p.length += int(k)
			}
			off += k
			left -= k
		}
		if control {
			p.emit(capture.WSMessage{Time: time.Now(), FromClient: p.fromClient, Opcode: op, Data: cbuf, Length: len(cbuf)})
			continue
		}
		if fin {
			data, length := p.buf, p.length
			if p.compressed && p.deflate {
				data = p.inflate(data)
				length = len(data)
			}
			if len(data) > maxWSCapture {
				data = data[:maxWSCapture]
			}
			p.emit(capture.WSMessage{Time: time.Now(), FromClient: p.fromClient, Opcode: p.opcode, Data: append([]byte(nil), data...), Length: length})
			p.buf = p.buf[:0]
		}
	}
}

// inflate decompresses one permessage-deflate message (RFC 7692).
func (p *wsParser) inflate(data []byte) []byte {
	in := append(append([]byte(nil), data...), 0x00, 0x00, 0xff, 0xff)
	dict := p.window
	if p.noCtx {
		dict = nil
	}
	if p.fr == nil {
		p.fr = flate.NewReader(bytes.NewReader(in))
		if dict != nil {
			p.fr.(flate.Resetter).Reset(bytes.NewReader(in), dict)
		}
	} else {
		p.fr.(flate.Resetter).Reset(bytes.NewReader(in), dict)
	}
	out, _ := io.ReadAll(io.LimitReader(p.fr, 64<<20))
	if !p.noCtx {
		w := append(p.window, out...)
		if len(w) > 32<<10 {
			w = append([]byte(nil), w[len(w)-32<<10:]...)
		}
		p.window = w
	}
	return out
}
