package proxy

import (
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Rule is an AutoResponder rule: requests whose URL matches Match get
// Action applied. Match is a substring, a wildcard pattern with *, an
// exact URL with the "exact:" prefix, or a regular expression with the
// "regex:" prefix.
//
// Actions:
//
//	*block               answer 403 and mark the request blocked
//	*drop                close the connection without an answer
//	*404, *500, ...      answer with that status
//	*delay:ms            wait, then go on (other rules still apply)
//	*redir:URL           answer with a 302 redirect to URL
//	*header:Name=Value   set a request header, then go on
//	*resp-header:N=V     set a response header, then go on
//	*body:text           answer 200 with text as the body
//	http(s)://...        fetch this URL instead (regex groups as $1)
//	C:\path\file         answer 200 with the file's content
type Rule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Match   string `json:"match"`
	Method  string `json:"method,omitempty"`
	Action  string `json:"action"`
	Comment string `json:"comment,omitempty"`
}

type compiledRule struct {
	Rule
	re *regexp.Regexp
}

func compileRules(rs []Rule) []compiledRule {
	var out []compiledRule
	for _, r := range rs {
		if !r.Enabled || strings.TrimSpace(r.Match) == "" || strings.TrimSpace(r.Action) == "" {
			continue
		}
		c := compiledRule{Rule: r}
		m := strings.TrimSpace(r.Match)
		switch {
		case hasPrefixFold(m, "regex:"):
			re, err := regexp.Compile(m[len("regex:"):])
			if err != nil {
				continue
			}
			c.re = re
		case hasPrefixFold(m, "exact:"):
			c.re = regexp.MustCompile("^" + regexp.QuoteMeta(m[len("exact:"):]) + "$")
		case strings.Contains(m, "*"):
			c.re = regexp.MustCompile("(?i)" + strings.ReplaceAll(regexp.QuoteMeta(m), `\*`, ".*"))
		default:
			c.re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(m))
		}
		out = append(out, c)
	}
	return out
}

func hasPrefixFold(s, p string) bool {
	return len(s) >= len(p) && strings.EqualFold(s[:len(p)], p)
}

// MatchURL tests a pattern in rule syntax against a URL; the UI uses it to
// preview rules and breakpoint filters.
func MatchURL(pattern, u string) bool {
	if strings.TrimSpace(pattern) == "" {
		return true
	}
	cs := compileRules([]Rule{{Enabled: true, Match: pattern, Action: "x"}})
	return len(cs) == 1 && cs[0].re.MatchString(u)
}

// compilePatterns compiles patterns in rule syntax, one per line.
func compilePatterns(text string) []compiledRule {
	var rs []Rule
	for _, l := range SplitPatterns(text) {
		rs = append(rs, Rule{Enabled: true, Match: l, Action: "x"})
	}
	return compileRules(rs)
}

// SplitPatterns returns the non-empty lines of a pattern list.
func SplitPatterns(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// MatchAny tests patterns, one per line, against a URL. No patterns
// match everything.
func MatchAny(text, u string) bool {
	ps := compilePatterns(text)
	if len(ps) == 0 {
		return true
	}
	for _, p := range ps {
		if p.re.MatchString(u) {
			return true
		}
	}
	return false
}

// verdict is what the rules decided for one request.
type verdict struct {
	rule string // names of the rules that applied
	// A final answer; status 0 means none.
	status   int
	header   [][2]string
	body     []byte
	drop     bool
	blocked  bool
	redirect string // fetch this URL instead
	delay    time.Duration
	reqSet   [][2]string
	respSet  [][2]string
}

func (v *verdict) final() bool { return v.status != 0 || v.drop }

func (v *verdict) note(r compiledRule) {
	name := r.Match
	if r.Comment != "" {
		name = r.Comment
	}
	if v.rule != "" {
		v.rule += "; "
	}
	v.rule += name
}

// evaluate applies the rules to a request in order until a final action.
func evaluate(rules []compiledRule, method, u string) verdict {
	var v verdict
	for _, r := range rules {
		if r.Method != "" && !strings.EqualFold(r.Method, method) {
			continue
		}
		loc := r.re.FindStringSubmatchIndex(u)
		if loc == nil {
			continue
		}
		a := strings.TrimSpace(r.Action)
		low := strings.ToLower(a)
		switch {
		case low == "*block":
			v.status, v.blocked = 403, true
			v.header = [][2]string{{"Content-Type", "text/plain; charset=utf-8"}}
			v.body = []byte("Blocked by NLR Capture rule: " + r.Match + "\n")
		case low == "*drop" || low == "*reset":
			v.drop, v.blocked = true, true
		case strings.HasPrefix(low, "*delay:"):
			if ms, err := strconv.Atoi(strings.TrimSpace(a[7:])); err == nil && ms > 0 {
				v.delay += time.Duration(ms) * time.Millisecond
			}
			v.note(r)
			continue
		case strings.HasPrefix(low, "*redir:"):
			v.status = 302
			v.header = [][2]string{{"Location", strings.TrimSpace(a[7:])}, {"Content-Length", "0"}}
		case strings.HasPrefix(low, "*header:"), strings.HasPrefix(low, "*resp-header:"):
			kv := a[strings.IndexByte(a, ':')+1:]
			n, val, _ := strings.Cut(kv, "=")
			pair := [2]string{strings.TrimSpace(n), strings.TrimSpace(val)}
			if strings.HasPrefix(low, "*header:") {
				v.reqSet = append(v.reqSet, pair)
			} else {
				v.respSet = append(v.respSet, pair)
			}
			v.note(r)
			continue
		case strings.HasPrefix(low, "*body:"):
			body := a[6:]
			v.status = 200
			ct := "text/plain; charset=utf-8"
			if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
				ct = "application/json; charset=utf-8"
			} else if strings.HasPrefix(t, "<") {
				ct = "text/html; charset=utf-8"
			}
			v.header = [][2]string{{"Content-Type", ct}}
			v.body = []byte(body)
		case len(a) == 4 && a[0] == '*' && isDigits(a[1:]):
			code, _ := strconv.Atoi(a[1:])
			v.status = code
			v.header = [][2]string{{"Content-Type", "text/plain; charset=utf-8"}}
			v.body = []byte(strconv.Itoa(code) + " (NLR Capture)\n")
		case hasPrefixFold(a, "http://") || hasPrefixFold(a, "https://"):
			target := string(r.re.ExpandString(nil, a, u, loc))
			if _, err := url.Parse(target); err == nil {
				v.redirect = target
			}
			v.note(r)
			continue
		default:
			p := a
			if r.re != nil {
				p = string(r.re.ExpandString(nil, a, u, loc))
			}
			b, err := os.ReadFile(p)
			if err != nil {
				v.status = 502
				v.header = [][2]string{{"Content-Type", "text/plain; charset=utf-8"}}
				v.body = []byte("NLR Capture: cannot read " + p + ": " + err.Error() + "\n")
			} else {
				v.status = 200
				ct := mime.TypeByExtension(filepath.Ext(p))
				if ct == "" {
					ct = "application/octet-stream"
				}
				v.header = [][2]string{{"Content-Type", ct}}
				v.body = b
			}
		}
		v.note(r)
		if v.final() {
			return v
		}
	}
	return v
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// Breakpoints hold matching requests before they are sent and responses
// before they are returned, so they can be edited. Match lists patterns
// in rule syntax, one per line; a request matching any of them stops.
type Breakpoints struct {
	Request  bool   `json:"request"`
	Response bool   `json:"response"`
	Match    string `json:"match"`
}

// Throttle slows traffic down like the DevTools network conditions.
type Throttle struct {
	Name    string        `json:"name"`
	Latency time.Duration `json:"latency"`
	Down    int64         `json:"down"` // bytes per second, 0 for unlimited
	Up      int64         `json:"up"`
	Offline bool          `json:"offline"`
}

// ThrottlePresets are the choices the toolbar offers.
var ThrottlePresets = []Throttle{
	{Name: "无限制"},
	{Name: "快速 4G", Latency: 150 * time.Millisecond, Down: 9 * 1000 * 1000 / 8, Up: 1500 * 1000 / 8},
	{Name: "慢速 4G", Latency: 560 * time.Millisecond, Down: 1600 * 1000 / 8, Up: 750 * 1000 / 8},
	{Name: "3G", Latency: 2000 * time.Millisecond, Down: 400 * 1000 / 8, Up: 400 * 1000 / 8},
	{Name: "离线", Offline: true},
}
