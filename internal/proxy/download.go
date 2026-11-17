package proxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"nlrcapture/internal/capture"
)

// partialHeaders are dropped from a download so that the server sends the
// whole file, fresh and unencoded.
var partialHeaders = []string{
	"Range", "If-Range", "If-None-Match", "If-Modified-Since", "If-Match",
	"If-Unmodified-Since", "Accept-Encoding",
}

// Download sends a captured request again, without any Range or
// conditional headers, and streams the whole response body to w. It
// follows redirects. progress is called as bytes arrive; total is -1 when
// the size is unknown. The download stops when ctx is cancelled.
func (e *Engine) Download(ctx context.Context, s *capture.Session, w io.Writer, progress func(done, total int64)) error {
	if s.Kind == capture.KindTunnel {
		return errors.New("隧道请求无法下载")
	}
	h := capture.Header{}
	for _, f := range s.ReqHeader {
		if strings.HasPrefix(f.Name, ":") {
			if f.Name == ":authority" {
				h = append(h, capture.Field{Name: "Host", Value: f.Value})
			}
			continue
		}
		h = append(h, f)
	}
	for _, n := range partialHeaders {
		h = h.Del(n)
	}
	h = h.Set("Accept-Encoding", "identity")
	rawURL := s.URL
	if strings.HasPrefix(rawURL, "ws://") {
		rawURL = "http://" + rawURL[5:]
	} else if strings.HasPrefix(rawURL, "wss://") {
		rawURL = "https://" + rawURL[6:]
	}
	method, body := s.Method, s.ReqBody

	for hops := 0; ; hops++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := e.roundTripH1(ctx, method, rawURL, h, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		switch res.code {
		case 301, 302, 303, 307, 308:
			loc := res.head.header.Get("Location")
			res.up.Close()
			if loc == "" {
				return fmt.Errorf("服务器返回 %d 但没有 Location", res.code)
			}
			if hops >= 10 {
				return errors.New("重定向次数过多")
			}
			base, _ := url.Parse(rawURL)
			next, err := base.Parse(loc)
			if err != nil {
				return fmt.Errorf("无效的重定向地址：%s", loc)
			}
			if res.code == 303 || ((res.code == 301 || res.code == 302) && method == "POST") {
				method, body = "GET", nil
				h = h.Del("Content-Type")
			}
			h = h.Del("Host")
			if !strings.EqualFold(next.Host, base.Host) {
				h = h.Del("Cookie").Del("Authorization")
			}
			rawURL = next.String()
			continue
		}
		defer res.up.Close()
		stop := context.AfterFunc(ctx, func() { res.up.Close() })
		defer stop()
		if res.code < 200 || res.code > 299 {
			return fmt.Errorf("服务器返回 %d %s", res.code, res.reason)
		}
		if res.code == 206 {
			return errors.New("服务器只返回了部分内容 (206)")
		}
		total := int64(-1)
		if n, err := strconv.ParseInt(res.head.header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
			total = n
		}
		r := res.body
		if ce := strings.TrimSpace(res.head.header.Get("Content-Encoding")); ce != "" && !strings.EqualFold(ce, "identity") {
			dr, err := decodeStream(r, ce)
			if err != nil {
				return err
			}
			r = dr
			total = -1
		}
		pw := &progressWriter{w: w, total: total, fn: progress}
		if progress != nil {
			progress(0, total)
		}
		_, err = io.CopyBuffer(pw, r, make([]byte, 256<<10))
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && total >= 0 && pw.n < total {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
}

type progressWriter struct {
	w     io.Writer
	n     int64
	total int64
	fn    func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.fn != nil {
		p.fn(p.n, p.total)
	}
	return n, err
}

// decodeStream undoes the content encodings in ce while reading.
func decodeStream(r io.Reader, ce string) (io.Reader, error) {
	encs := strings.Split(strings.ToLower(ce), ",")
	for i := len(encs) - 1; i >= 0; i-- {
		switch e := strings.TrimSpace(encs[i]); e {
		case "", "identity", "none":
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(r)
			if err != nil {
				return nil, err
			}
			r = zr
		case "deflate":
			br := bufio.NewReader(r)
			if b, err := br.Peek(2); err == nil && (uint16(b[0])<<8|uint16(b[1]))%31 == 0 && b[0]&0x0f == 8 {
				zr, err := zlib.NewReader(br)
				if err != nil {
					return nil, err
				}
				r = zr
			} else {
				r = flate.NewReader(br)
			}
		case "br":
			r = brotli.NewReader(r)
		case "zstd":
			zr, err := zstd.NewReader(r)
			if err != nil {
				return nil, err
			}
			r = zr.IOReadCloser()
		default:
			return nil, errors.New("不支持的内容编码 " + e)
		}
	}
	return r, nil
}
