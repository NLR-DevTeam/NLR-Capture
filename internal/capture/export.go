package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// hopHeaders are left out when a request is turned into a command: the
// tools set them themselves.
var skipInCommand = map[string]bool{
	"content-length": true, "host": true, "connection": true,
	"proxy-connection": true, "keep-alive": true, "transfer-encoding": true,
}

func commandHeaders(s *Session) []Field {
	var out []Field
	for _, f := range s.ReqHeader {
		n := strings.ToLower(f.Name)
		if skipInCommand[n] || strings.HasPrefix(n, ":") {
			continue
		}
		out = append(out, f)
	}
	return out
}

func bashQuote(s string) string {
	if !strings.ContainsAny(s, "'\n\r\\") && utf8.ValidString(s) {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteString("$'")
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteString("'")
	return b.String()
}

func cmdQuote(s string) string {
	// cmd.exe: double quotes, with ^ escaping for the special characters
	// cmd still interprets inside them when the line is pasted.
	r := strings.NewReplacer(`"`, `""`, `%`, `%%`, "\r\n", "^\r\n", "\n", "^\n")
	return `"` + r.Replace(s) + `"`
}

// CurlBash returns the request as a curl command for bash.
func CurlBash(s *Session) string { return curl(s, bashQuote, " \\\n  ") }

// CurlCmd returns the request as a curl command for cmd.exe.
func CurlCmd(s *Session) string { return curl(s, cmdQuote, " ^\n  ") }

func curl(s *Session, q func(string) string, sep string) string {
	parts := []string{"curl " + q(s.URL)}
	m := s.Method
	body, _ := RequestBody(s)
	if m != "GET" && !(m == "POST" && len(body) > 0) {
		parts = append(parts, "-X "+q(m))
	}
	for _, f := range commandHeaders(s) {
		if strings.EqualFold(f.Name, "Content-Encoding") && !bytes.Equal(body, s.ReqBody) {
			continue
		}
		parts = append(parts, "-H "+q(f.Name+": "+f.Value))
	}
	if len(body) > 0 {
		parts = append(parts, "--data-raw "+q(string(body)))
	}
	if strings.Contains(strings.ToLower(s.ReqHeader.Get("Accept-Encoding")), "gzip") {
		parts = append(parts, "--compressed")
	}
	return strings.Join(parts, sep)
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// PowerShell returns the request as an Invoke-WebRequest command.
func PowerShell(s *Session) string {
	var b strings.Builder
	b.WriteString("$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession\n")
	b.WriteString("Invoke-WebRequest -UseBasicParsing -Uri " + psQuote(s.URL) + " `\n")
	b.WriteString("-Method " + psQuote(s.Method) + " `\n")
	b.WriteString("-WebSession $session `\n")
	var ct string
	b.WriteString("-Headers @{\n")
	for _, f := range commandHeaders(s) {
		if strings.EqualFold(f.Name, "Content-Type") {
			ct = f.Value
			continue
		}
		if strings.EqualFold(f.Name, "User-Agent") {
			continue
		}
		b.WriteString("  " + psQuote(f.Name) + " = " + psQuote(f.Value) + "\n")
	}
	b.WriteString("}")
	if ua := s.ReqHeader.Get("User-Agent"); ua != "" {
		b.WriteString(" `\n-UserAgent " + psQuote(ua))
	}
	if ct != "" {
		b.WriteString(" `\n-ContentType " + psQuote(ct))
	}
	if body, _ := RequestBody(s); len(body) > 0 {
		b.WriteString(" `\n-Body " + psQuote(string(body)))
	}
	return b.String()
}

// Fetch returns the request as a JavaScript fetch call.
func Fetch(s *Session) string {
	headers := map[string]string{}
	for _, f := range commandHeaders(s) {
		n := strings.ToLower(f.Name)
		if n == "cookie" || n == "user-agent" || n == "accept-encoding" || strings.HasPrefix(n, "sec-") || n == "referer" || n == "origin" {
			continue
		}
		headers[n] = f.Value
	}
	opts := map[string]any{
		"headers": headers,
		"method":  s.Method,
	}
	if ref := s.ReqHeader.Get("Referer"); ref != "" {
		opts["referrer"] = ref
	}
	if body, _ := RequestBody(s); len(body) > 0 {
		opts["body"] = string(body)
	} else {
		opts["body"] = nil
	}
	if s.ReqHeader.Has("Cookie") {
		opts["credentials"] = "include"
	} else {
		opts["credentials"] = "omit"
	}
	opts["mode"] = "cors"
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(opts)
	u, _ := json.Marshal(s.URL)
	return "fetch(" + string(u) + ", " + strings.TrimSpace(buf.String()) + ");"
}

// RawRequest returns the request as HTTP/1.1 text. With decoded set the
// body is shown without content encoding.
func RawRequest(s *Session, decoded bool) []byte {
	var b bytes.Buffer
	target := s.Path()
	if s.Kind == KindTunnel {
		target = s.Host
	}
	proto := s.Proto
	if proto == "" || proto == "h2" {
		proto = "HTTP/1.1"
	}
	if s.Proto == "h2" {
		proto = "HTTP/2"
	}
	fmt.Fprintf(&b, "%s %s %s\r\n", s.Method, target, proto)
	host := false
	for _, f := range s.ReqHeader {
		if strings.EqualFold(f.Name, "Host") {
			host = true
		}
	}
	if !host && s.Host != "" {
		fmt.Fprintf(&b, "Host: %s\r\n", s.Host)
	}
	for _, f := range s.ReqHeader {
		if strings.HasPrefix(f.Name, ":") {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", f.Name, f.Value)
	}
	b.WriteString("\r\n")
	body := s.ReqBody
	if decoded {
		body, _ = RequestBody(s)
	}
	b.Write(body)
	return b.Bytes()
}

// RawResponse returns the response as HTTP/1.1 text.
func RawResponse(s *Session, decoded bool) []byte {
	if s.Status == 0 {
		return nil
	}
	var b bytes.Buffer
	proto := s.RespProto
	if proto == "" || proto == "h2" {
		proto = "HTTP/1.1"
	}
	if s.RespProto == "h2" {
		proto = "HTTP/2"
	}
	reason := s.Reason
	if reason == "" {
		reason = StatusText(s.Status)
	}
	fmt.Fprintf(&b, "%s %d %s\r\n", proto, s.Status, reason)
	for _, f := range s.RespHeader {
		if strings.HasPrefix(f.Name, ":") {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", f.Name, f.Value)
	}
	b.WriteString("\r\n")
	body := s.RespBody
	if decoded {
		body, _ = ResponseBody(s)
	}
	b.Write(body)
	return b.Bytes()
}

// HeaderText returns header fields as "Name: Value" lines.
func HeaderText(h Header) string {
	var b strings.Builder
	for _, f := range h {
		b.WriteString(f.Name + ": " + f.Value + "\n")
	}
	return b.String()
}

// ParseRawRequest reads an edited HTTP/1.x request in text form, as
// produced by RawRequest. The body is everything after the blank line.
func ParseRawRequest(raw string) (method, target, proto string, h Header, body []byte, err error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	head, rest, _ := strings.Cut(raw, "\n\n")
	lines := strings.Split(head, "\n")
	first := strings.Fields(lines[0])
	if len(first) < 2 {
		return "", "", "", nil, nil, fmt.Errorf("请求行无效：%q", lines[0])
	}
	method, target = first[0], first[1]
	proto = "HTTP/1.1"
	if len(first) > 2 {
		proto = first[2]
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		n, v, ok := strings.Cut(l, ":")
		if !ok {
			return "", "", "", nil, nil, fmt.Errorf("标头无效：%q", l)
		}
		h = append(h, Field{strings.TrimSpace(n), strings.TrimSpace(v)})
	}
	return method, target, proto, h, []byte(rest), nil
}

// ParseRawResponse reads an edited HTTP/1.x response in text form.
func ParseRawResponse(raw string) (status int, reason string, h Header, body []byte, err error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	head, rest, _ := strings.Cut(raw, "\n\n")
	lines := strings.Split(head, "\n")
	first := strings.SplitN(strings.TrimSpace(lines[0]), " ", 3)
	if len(first) < 2 {
		return 0, "", nil, nil, fmt.Errorf("状态行无效：%q", lines[0])
	}
	status, err = strconv.Atoi(first[1])
	if err != nil || status < 100 || status > 999 {
		return 0, "", nil, nil, fmt.Errorf("状态码无效：%q", first[1])
	}
	if len(first) > 2 {
		reason = first[2]
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		n, v, ok := strings.Cut(l, ":")
		if !ok {
			return 0, "", nil, nil, fmt.Errorf("标头无效：%q", l)
		}
		h = append(h, Field{strings.TrimSpace(n), strings.TrimSpace(v)})
	}
	return status, reason, h, []byte(rest), nil
}
