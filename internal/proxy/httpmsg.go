package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"nlrcapture/internal/capture"
)

// This file reads and writes HTTP/1.x messages by hand rather than with
// net/http: a debugging proxy must pass messages on as they are, keeping
// header order and case, and must see the framing to time each phase.

const maxHeaderBytes = 1 << 20

var errHeaderTooLarge = errors.New("标头过大")

// head is the start line and header block of a message.
type head struct {
	line   string
	header capture.Header
	size   int // bytes on the wire
}

// readHead reads a start line and header fields up to the blank line.
func readHead(br *bufio.Reader) (*head, error) {
	h := &head{}
	first := true
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// A very long line: collect it piecewise.
			buf := append([]byte(nil), line...)
			for err == bufio.ErrBufferFull {
				line, err = br.ReadSlice('\n')
				buf = append(buf, line...)
				if len(buf) > maxHeaderBytes {
					return nil, errHeaderTooLarge
				}
			}
			line = buf
		}
		if err != nil {
			if err == io.EOF && first && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, err
		}
		h.size += len(line)
		if h.size > maxHeaderBytes {
			return nil, errHeaderTooLarge
		}
		s := strings.TrimRight(string(line), "\r\n")
		if first {
			if s == "" {
				// Tolerate stray blank lines between messages.
				h.size = 0
				continue
			}
			h.line = s
			first = false
			continue
		}
		if s == "" {
			return h, nil
		}
		if (s[0] == ' ' || s[0] == '\t') && len(h.header) > 0 {
			// Obsolete line folding.
			h.header[len(h.header)-1].Value += " " + strings.TrimSpace(s)
			continue
		}
		name, value, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("标头行无效：%q", s)
		}
		h.header = append(h.header, capture.Field{Name: strings.TrimRight(name, " \t"), Value: strings.TrimSpace(value)})
	}
}

// requestLine splits "GET /path HTTP/1.1".
func requestLine(s string) (method, target, proto string, err error) {
	parts := strings.SplitN(s, " ", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/") {
		return "", "", "", fmt.Errorf("请求行无效：%q", s)
	}
	return parts[0], parts[1], parts[2], nil
}

// statusLine splits "HTTP/1.1 200 OK".
func statusLine(s string) (proto string, code int, reason string, err error) {
	parts := strings.SplitN(s, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return "", 0, "", fmt.Errorf("状态行无效：%q", s)
	}
	code, err = strconv.Atoi(parts[1])
	if err != nil || code < 100 || code > 999 {
		return "", 0, "", fmt.Errorf("状态码无效：%q", s)
	}
	if len(parts) == 3 {
		reason = parts[2]
	}
	return parts[0], code, reason, nil
}

// framing says how a body is delimited.
type framing struct {
	chunked bool
	length  int64 // -1: unknown; read to close (responses only)
}

func hasToken(h capture.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func requestFraming(h capture.Header) (framing, error) {
	if te := h.Get("Transfer-Encoding"); te != "" {
		if hasToken(h, "Transfer-Encoding", "chunked") {
			return framing{chunked: true, length: -1}, nil
		}
		return framing{}, fmt.Errorf("不支持的 Transfer-Encoding：%s", te)
	}
	if cl := h.Get("Content-Length"); cl != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(cl), 10, 64)
		if err != nil || n < 0 {
			return framing{}, fmt.Errorf("Content-Length 无效：%q", cl)
		}
		return framing{length: n}, nil
	}
	return framing{length: 0}, nil
}

func responseFraming(method string, code int, h capture.Header) framing {
	if method == "HEAD" || code == 204 || code == 304 || (code >= 100 && code < 200) {
		return framing{length: 0}
	}
	if method == "CONNECT" && code/100 == 2 {
		return framing{length: 0}
	}
	if hasToken(h, "Transfer-Encoding", "chunked") {
		return framing{chunked: true, length: -1}
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(strings.Split(cl, ",")[0]), 10, 64); err == nil && n >= 0 {
			return framing{length: n}
		}
	}
	return framing{length: -1}
}

// bodyReader returns a reader for a body with the given framing. It yields
// the payload with chunk framing removed.
func bodyReader(br *bufio.Reader, f framing) io.Reader {
	switch {
	case f.chunked:
		return &chunkedReader{br: br}
	case f.length >= 0:
		return io.LimitReader(br, f.length)
	}
	return br
}

// chunkedReader decodes the chunked transfer coding.
type chunkedReader struct {
	br   *bufio.Reader
	left int64
	done bool
	err  error
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.done {
		return 0, io.EOF
	}
	if c.left == 0 {
		line, err := readLine(c.br)
		if err != nil {
			c.err = err
			return 0, err
		}
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		n, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
		if err != nil || n < 0 {
			c.err = fmt.Errorf("分块长度无效：%q", line)
			return 0, c.err
		}
		if n == 0 {
			// Trailer fields until the blank line; they are dropped.
			for {
				l, err := readLine(c.br)
				if err != nil {
					c.err = err
					return 0, err
				}
				if l == "" {
					break
				}
			}
			c.done = true
			return 0, io.EOF
		}
		c.left = n
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.br.Read(p)
	c.left -= int64(n)
	if c.left == 0 && err == nil {
		if _, err := readLine(c.br); err != nil {
			c.err = err
		}
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		c.err = err
	}
	return n, err
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	if len(line) > 4096 {
		return "", errors.New("分块行过长")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// chunkedWriter writes the chunked transfer coding.
type chunkedWriter struct {
	w io.Writer
}

func (c chunkedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := fmt.Fprintf(c.w, "%x\r\n", len(p)); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	if err != nil {
		return n, err
	}
	_, err = io.WriteString(c.w, "\r\n")
	return n, err
}

func (c chunkedWriter) Close() error {
	_, err := io.WriteString(c.w, "0\r\n\r\n")
	return err
}

// writeHead writes a start line and header block.
func writeHead(w *bufio.Writer, line string, h capture.Header) int {
	n := len(line) + 2
	w.WriteString(line)
	w.WriteString("\r\n")
	for _, f := range h {
		w.WriteString(f.Name)
		w.WriteString(": ")
		w.WriteString(f.Value)
		w.WriteString("\r\n")
		n += len(f.Name) + len(f.Value) + 4
	}
	w.WriteString("\r\n")
	return n + 2
}

// hop-by-hop headers that a proxy must not forward.
func stripHopHeaders(h capture.Header) capture.Header {
	// Headers named in Connection are hop-by-hop too.
	var extra []string
	for _, v := range h.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				extra = append(extra, t)
			}
		}
	}
	out := h[:0:0]
	for _, f := range h {
		switch strings.ToLower(f.Name) {
		case "proxy-connection", "proxy-authorization", "keep-alive", "te", "trailer":
			continue
		}
		skip := false
		for _, e := range extra {
			if strings.EqualFold(e, f.Name) && !strings.EqualFold(e, "upgrade") && !strings.EqualFold(e, "close") && !strings.EqualFold(e, "keep-alive") {
				skip = true
			}
		}
		if !skip {
			out = append(out, f)
		}
	}
	return out
}

// capBuffer keeps the first limit bytes written to it and counts the rest.
type capBuffer struct {
	buf   []byte
	limit int64
	n     int64
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if room := c.limit - int64(len(c.buf)); room > 0 {
		if int64(len(p)) > room {
			c.buf = append(c.buf, p[:room]...)
		} else {
			c.buf = append(c.buf, p...)
		}
	}
	return len(p), nil
}

func (c *capBuffer) truncated() bool { return c.n > int64(len(c.buf)) }

// bytes returns a copy of the captured bytes.
func (c *capBuffer) bytes() []byte {
	if len(c.buf) == 0 {
		return nil
	}
	return append([]byte(nil), c.buf...)
}
