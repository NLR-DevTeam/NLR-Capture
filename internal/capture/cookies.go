package capture

import (
	"strings"
)

// Cookie is one cookie: sent in a Cookie header, or set by Set-Cookie with
// its attributes.
type Cookie struct {
	Name, Value string
	Domain      string
	Path        string
	Expires     string
	MaxAge      string
	HTTPOnly    bool
	Secure      bool
	SameSite    string
	Partitioned bool
	Raw         string
}

// ParseCookies returns the cookies of the Cookie headers in h.
func ParseCookies(h Header) []Cookie {
	var out []Cookie
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, v, _ := strings.Cut(part, "=")
			out = append(out, Cookie{Name: strings.TrimSpace(n), Value: strings.TrimSpace(v), Raw: part})
		}
	}
	return out
}

// ParseSetCookies returns the cookies set by the Set-Cookie headers in h.
func ParseSetCookies(h Header) []Cookie {
	var out []Cookie
	for _, line := range h.Values("Set-Cookie") {
		parts := strings.Split(line, ";")
		n, v, _ := strings.Cut(parts[0], "=")
		c := Cookie{Name: strings.TrimSpace(n), Value: strings.TrimSpace(v), Raw: line}
		for _, a := range parts[1:] {
			k, val, _ := strings.Cut(strings.TrimSpace(a), "=")
			val = strings.TrimSpace(val)
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "domain":
				c.Domain = val
			case "path":
				c.Path = val
			case "expires":
				c.Expires = val
			case "max-age":
				c.MaxAge = val
			case "httponly":
				c.HTTPOnly = true
			case "secure":
				c.Secure = true
			case "samesite":
				c.SameSite = val
			case "partitioned":
				c.Partitioned = true
			}
		}
		out = append(out, c)
	}
	return out
}
