package capture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"container/list"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/text/encoding/htmlindex"
)

// maxDecoded bounds a decoded body, against compression bombs.
const maxDecoded = 64 << 20

// Decode undoes the content encodings named in ce (as in a
// Content-Encoding header, applied in order).
func Decode(body []byte, ce string) ([]byte, error) {
	encs := strings.Split(strings.ToLower(ce), ",")
	out := body
	for i := len(encs) - 1; i >= 0; i-- {
		e := strings.TrimSpace(encs[i])
		var r io.Reader
		switch e {
		case "", "identity", "none":
			continue
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(bytes.NewReader(out))
			if err != nil {
				return body, err
			}
			r = zr
		case "deflate":
			// Servers send both zlib-wrapped and raw deflate.
			if zr, err := zlib.NewReader(bytes.NewReader(out)); err == nil {
				r = zr
			} else {
				r = flate.NewReader(bytes.NewReader(out))
			}
		case "br":
			r = brotli.NewReader(bytes.NewReader(out))
		case "zstd":
			zr, err := zstd.NewReader(bytes.NewReader(out), zstd.WithDecoderMaxMemory(maxDecoded))
			if err != nil {
				return body, err
			}
			defer zr.Close()
			r = zr
		default:
			return body, errors.New("unsupported content encoding " + e)
		}
		b, err := io.ReadAll(io.LimitReader(r, maxDecoded))
		if err != nil && len(b) == 0 {
			return body, err
		}
		out = b
	}
	return out, nil
}

// decodedCache keeps recently decoded bodies, keyed by session and side.
type decodedKey struct {
	id   int64
	resp bool
	body *byte // first byte of the raw body, to notice a replaced body
	n    int
}

type decodedEntry struct {
	key  decodedKey
	data []byte
	err  error
}

var cache = struct {
	sync.Mutex
	lru   *list.List
	items map[decodedKey]*list.Element
	size  int
}{lru: list.New(), items: map[decodedKey]*list.Element{}}

const cacheBudget = 256 << 20

// ResponseBody returns the response body with content encodings removed.
func ResponseBody(s *Session) ([]byte, error) {
	return decodedBody(s, true)
}

// RequestBody returns the request body with content encodings removed.
func RequestBody(s *Session) ([]byte, error) {
	return decodedBody(s, false)
}

func decodedBody(s *Session, resp bool) ([]byte, error) {
	raw, h := s.ReqBody, s.ReqHeader
	if resp {
		raw, h = s.RespBody, s.RespHeader
	}
	ce := h.Get("Content-Encoding")
	if len(raw) == 0 || ce == "" || s.BodyDecoded {
		return raw, nil
	}
	k := decodedKey{id: s.ID, resp: resp, body: &raw[0], n: len(raw)}
	cache.Lock()
	if el, ok := cache.items[k]; ok {
		cache.lru.MoveToFront(el)
		e := el.Value.(*decodedEntry)
		cache.Unlock()
		return e.data, e.err
	}
	cache.Unlock()
	data, err := Decode(raw, ce)
	if err != nil && (resp && s.RespTruncated || !resp && s.ReqTruncated) {
		err = errors.New("正文被截断，无法完整解码")
	}
	cache.Lock()
	el := cache.lru.PushFront(&decodedEntry{key: k, data: data, err: err})
	cache.items[k] = el
	cache.size += len(data)
	for cache.size > cacheBudget && cache.lru.Len() > 1 {
		last := cache.lru.Back()
		e := last.Value.(*decodedEntry)
		cache.lru.Remove(last)
		delete(cache.items, e.key)
		cache.size -= len(e.data)
	}
	cache.Unlock()
	return data, err
}

// Charset returns the charset parameter of a Content-Type value.
func Charset(ct string) string {
	for _, p := range strings.Split(ct, ";")[1:] {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "charset") {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// ToUTF8 converts text in the given charset to UTF-8. Text that is already
// valid UTF-8, or in an unknown charset, is returned as is.
func ToUTF8(b []byte, charset string) []byte {
	cs := strings.ToLower(charset)
	if cs == "" || cs == "utf-8" || cs == "utf8" || cs == "us-ascii" {
		return b
	}
	enc, err := htmlindex.Get(cs)
	if err != nil {
		return b
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return b
	}
	return out
}

// BodyText returns a body as UTF-8 text for display and search, using the
// charset of its Content-Type. ok is false for binary content.
func BodyText(s *Session, resp bool) (text string, ok bool, err error) {
	b, err := decodedBody(s, resp)
	h := s.ReqHeader
	if resp {
		h = s.RespHeader
	}
	ct := h.Get("Content-Type")
	if !IsTextual(mediaType(ct), b) {
		return "", false, err
	}
	return string(ToUTF8(b, Charset(ct))), true, err
}

// IsTextual reports whether a body of the given media type should be shown
// as text. Unknown types are judged by their content.
func IsTextual(mt string, b []byte) bool {
	switch {
	case strings.HasPrefix(mt, "text/"),
		strings.Contains(mt, "json"), strings.Contains(mt, "xml"),
		strings.Contains(mt, "javascript"), strings.Contains(mt, "ecmascript"),
		strings.Contains(mt, "x-www-form-urlencoded"), strings.Contains(mt, "graphql"),
		strings.HasSuffix(mt, "+yaml"), mt == "application/x-ndjson",
		mt == "application/manifest+json", mt == "image/svg+xml":
		return true
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "audio/"),
		strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "font/"),
		mt == "application/octet-stream", mt == "application/wasm",
		mt == "application/zip", mt == "application/pdf",
		strings.Contains(mt, "protobuf"), strings.Contains(mt, "grpc"):
		return false
	}
	return LooksText(b)
}

// LooksText reports whether b reads as text: valid UTF-8 (or a legacy
// charset) without control characters other than whitespace.
func LooksText(b []byte) bool {
	if len(b) > 4096 {
		b = b[:4096]
	}
	if len(b) == 0 {
		return true
	}
	ctrl := 0
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' && c != 0x1b {
			ctrl++
		}
	}
	if ctrl > 0 {
		return false
	}
	if utf8.Valid(b) {
		return true
	}
	// A cut at the end may split a character.
	for i := 1; i <= 3 && i < len(b); i++ {
		if utf8.Valid(b[:len(b)-i]) {
			return true
		}
	}
	// Legacy charsets such as GBK have no control bytes either.
	return true
}
