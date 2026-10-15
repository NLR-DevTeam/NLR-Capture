package capture

import (
	"regexp"
	"strconv"
	"strings"
)

// FilterKeys are the property filters understood in the filter box, as in
// the DevTools network filter, plus process: for the owning program.
var FilterKeys = []string{
	"domain:", "method:", "status-code:", "mime-type:", "resource-type:",
	"larger-than:", "has-response-header:", "has-request-header:", "is:",
	"scheme:", "url:", "process:", "set-cookie-name:", "set-cookie-value:",
	"set-cookie-domain:", "cookie-name:", "cookie-value:", "header:", "body:",
}

type term struct {
	key   string // "" for free text
	value string
	neg   bool
	re    *regexp.Regexp
}

// Filter decides which sessions the request list shows.
type Filter struct {
	terms []term
	// Types limits the list to resource types (keys like TypeAPI); empty
	// shows every type.
	Types       map[string]bool
	Invert      bool
	HideTunnels bool
	OnlyErrors  bool
}

// SetText parses the filter box text.
func (f *Filter) SetText(text string) {
	f.terms = parseTerms(text)
}

// Empty reports whether the filter lets everything through.
func (f *Filter) Empty() bool {
	return len(f.terms) == 0 && len(f.Types) == 0 && !f.HideTunnels && !f.OnlyErrors
}

func splitFields(s string) []string {
	var out []string
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && (c == ' ' || c == '\t'):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func parseTerms(text string) []term {
	var terms []term
	for _, tok := range splitFields(text) {
		t := term{}
		if len(tok) > 1 && tok[0] == '-' {
			t.neg = true
			tok = tok[1:]
		}
		// A regular expression in slashes matches the URL.
		if len(tok) > 2 && tok[0] == '/' && tok[len(tok)-1] == '/' {
			if re, err := regexp.Compile("(?i)" + tok[1:len(tok)-1]); err == nil {
				t.re = re
				terms = append(terms, t)
				continue
			}
		}
		if k, v, ok := strings.Cut(tok, ":"); ok && v != "" && knownKey(k) {
			t.key, t.value = strings.ToLower(k), v
		} else {
			if t.neg && tok == "" {
				continue
			}
			t.value = tok
		}
		terms = append(terms, t)
	}
	return terms
}

func knownKey(k string) bool {
	k = strings.ToLower(k) + ":"
	for _, x := range FilterKeys {
		if x == k {
			return true
		}
	}
	return false
}

// Match reports whether s passes the filter.
func (f *Filter) Match(s *Session) bool {
	if f.HideTunnels && s.Kind == KindTunnel {
		return false
	}
	if f.OnlyErrors && !(s.Failed() || s.Status >= 400) {
		return false
	}
	if len(f.Types) > 0 {
		t := ResourceType(s)
		if !f.Types[t] {
			return false
		}
	}
	ok := true
	for _, t := range f.terms {
		m := t.match(s)
		if t.neg {
			m = !m
		}
		if !m {
			ok = false
			break
		}
	}
	if f.Invert && len(f.terms) > 0 {
		return !ok
	}
	return ok
}

// wildcard matches s against a pattern where * stands for any text.
func wildcard(pattern, s string) bool {
	pattern, s = strings.ToLower(pattern), strings.ToLower(s)
	if !strings.Contains(pattern, "*") {
		return s == pattern || strings.HasSuffix(s, "."+pattern)
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		p := parts[i]
		if i == len(parts)-1 {
			return strings.HasSuffix(s, p)
		}
		j := strings.Index(s, p)
		if j < 0 {
			return false
		}
		s = s[j+len(p):]
	}
	return true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// ParseSize reads sizes like 100, 10k, 1.5M.
func ParseSize(v string) (int64, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	mul := 1.0
	switch {
	case strings.HasSuffix(v, "k"), strings.HasSuffix(v, "kb"):
		mul, v = 1024, strings.TrimRight(v, "kb")
	case strings.HasSuffix(v, "m"), strings.HasSuffix(v, "mb"):
		mul, v = 1024*1024, strings.TrimRight(v, "mb")
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return int64(f * mul), true
}

func (t term) match(s *Session) bool {
	if t.re != nil {
		return t.re.MatchString(s.URL)
	}
	v := t.value
	switch t.key {
	case "":
		return containsFold(s.URL, v) || (s.Kind == KindTunnel && containsFold(s.Host, v))
	case "domain":
		return wildcard(v, s.Hostname())
	case "method":
		return strings.EqualFold(s.Method, v)
	case "status-code":
		if s.Status == 0 {
			return false
		}
		code := strconv.Itoa(s.Status)
		if strings.ContainsAny(v, "xX*") {
			// 4xx style ranges.
			if len(v) != 3 {
				return false
			}
			for i := range 3 {
				if v[i] != 'x' && v[i] != 'X' && v[i] != '*' && v[i] != code[i] {
					return false
				}
			}
			return true
		}
		return code == v
	case "mime-type":
		return wildcard(v, s.ContentType()) || containsFold(s.ContentType(), v)
	case "resource-type":
		rt := ResourceType(s)
		return strings.EqualFold(rt, v) || (rt == TypeAPI && (strings.EqualFold(v, "fetch") || strings.EqualFold(v, "xhr")))
	case "larger-than":
		n, ok := ParseSize(v)
		return ok && s.Transferred() > n
	case "has-response-header":
		return s.RespHeader.Has(v)
	case "has-request-header":
		return s.ReqHeader.Has(v)
	case "header":
		// header:name=value or header:value, over both directions.
		name, val, hasVal := strings.Cut(v, "=")
		for _, h := range []Header{s.ReqHeader, s.RespHeader} {
			for _, fl := range h {
				if hasVal {
					if strings.EqualFold(fl.Name, name) && containsFold(fl.Value, val) {
						return true
					}
				} else if containsFold(fl.Name, v) || containsFold(fl.Value, v) {
					return true
				}
			}
		}
		return false
	case "body":
		for _, resp := range []bool{false, true} {
			if txt, ok, _ := BodyText(s, resp); ok && containsFold(txt, v) {
				return true
			}
		}
		return false
	case "is":
		switch strings.ToLower(v) {
		case "running":
			return s.Running()
		case "failed", "error":
			return s.Failed()
		case "blocked":
			return s.State == StateBlocked
		case "paused":
			return s.State == StatePaused
		case "decrypted":
			return s.Decrypted
		case "tunnel":
			return s.Kind == KindTunnel
		case "websocket":
			return s.Kind == KindWebSocket
		case "mocked":
			return s.Rule != ""
		case "imported":
			return s.Source == SourceImport
		}
		return false
	case "scheme":
		return strings.EqualFold(s.Scheme, v)
	case "url":
		return containsFold(s.URL, v)
	case "process":
		return containsFold(s.Process, v) || strconv.Itoa(s.PID) == v
	case "set-cookie-name", "set-cookie-value", "set-cookie-domain":
		for _, c := range ParseSetCookies(s.RespHeader) {
			switch t.key {
			case "set-cookie-name":
				if strings.EqualFold(c.Name, v) {
					return true
				}
			case "set-cookie-value":
				if c.Value == v {
					return true
				}
			default:
				if wildcard(strings.TrimPrefix(v, "."), strings.TrimPrefix(c.Domain, ".")) {
					return true
				}
			}
		}
		return false
	case "cookie-name", "cookie-value":
		for _, c := range ParseCookies(s.ReqHeader) {
			if t.key == "cookie-name" && strings.EqualFold(c.Name, v) || t.key == "cookie-value" && c.Value == v {
				return true
			}
		}
		return false
	}
	return false
}
