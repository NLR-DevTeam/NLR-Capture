package capture

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Places a search hit can be in.
const (
	WhereURL        = "URL"
	WhereReqHeader  = "请求标头"
	WhereRespHeader = "响应标头"
	WhereReqBody    = "请求正文"
	WhereRespBody   = "响应正文"
	WhereWS         = "消息"
)

// SearchOptions say what to look for.
type SearchOptions struct {
	Query         string
	CaseSensitive bool
	Regex         bool
}

// SearchHit is one matching line.
type SearchHit struct {
	Where string
	Line  int // 1-based line in a body, 0 elsewhere
	// Text is the matching line, shortened around the match; Start and End
	// are byte offsets of the match within it.
	Text       string
	Start, End int
}

// SearchResult holds the hits in one session.
type SearchResult struct {
	Session *Session
	Hits    []SearchHit
	// More counts hits that were left out.
	More int
}

const maxHitsPerSession = 40

type matcher func(s string) [][]int

func newMatcher(o SearchOptions) (matcher, error) {
	if o.Query == "" {
		return nil, errors.New("empty query")
	}
	if o.Regex {
		pat := o.Query
		if !o.CaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, err
		}
		return func(s string) [][]int { return re.FindAllStringIndex(s, 8) }, nil
	}
	q := o.Query
	if o.CaseSensitive {
		return func(s string) [][]int { return findAll(s, q) }, nil
	}
	lq := strings.ToLower(q)
	return func(s string) [][]int {
		ls := strings.ToLower(s)
		// Lowering can change byte lengths for some scripts; fall back to
		// a regular expression then.
		if len(ls) != len(s) {
			re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(q))
			return re.FindAllStringIndex(s, 8)
		}
		return findAll(ls, lq)
	}, nil
}

func findAll(s, sub string) [][]int {
	var out [][]int
	off := 0
	for len(out) < 8 {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			break
		}
		out = append(out, []int{off + i, off + i + len(sub)})
		off += i + max(len(sub), 1)
		if off >= len(s) {
			break
		}
	}
	return out
}

// snippet shortens line around [a,b) and returns the new offsets.
func snippet(line string, a, b int) (string, int, int) {
	const ctx = 60
	start, end := max(a-ctx, 0), min(b+ctx*2, len(line))
	for start > 0 && !utf8.RuneStart(line[start]) {
		start--
	}
	for end < len(line) && !utf8.RuneStart(line[end]) {
		end++
	}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	text := prefix + line[start:end]
	shift := len(prefix) - start
	text = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, text)
	return text, a + shift, b + shift
}

// Search looks for o.Query in the given sessions and reports each session
// with hits through emit, in order. It stops early when ctx is cancelled.
func Search(ctx context.Context, sessions []*Session, o SearchOptions, emit func(SearchResult)) error {
	m, err := newMatcher(o)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r := SearchResult{Session: s}
		add := func(where string, line int, text string) {
			for _, loc := range m(text) {
				if len(r.Hits) >= maxHitsPerSession {
					r.More++
					return
				}
				t, a, b := snippet(text, loc[0], loc[1])
				r.Hits = append(r.Hits, SearchHit{Where: where, Line: line, Text: t, Start: a, End: b})
			}
		}
		add(WhereURL, 0, s.URL)
		for _, f := range s.ReqHeader {
			add(WhereReqHeader, 0, f.Name+": "+f.Value)
		}
		for _, f := range s.RespHeader {
			add(WhereRespHeader, 0, f.Name+": "+f.Value)
		}
		for _, resp := range []bool{false, true} {
			txt, ok, _ := BodyText(s, resp)
			if !ok || txt == "" {
				continue
			}
			where := WhereReqBody
			if resp {
				where = WhereRespBody
			}
			// Quick reject before splitting into lines.
			if len(m(txt)) == 0 {
				continue
			}
			for i, line := range strings.Split(txt, "\n") {
				add(where, i+1, line)
				if r.More > 0 {
					break
				}
			}
		}
		for _, msg := range s.WS {
			if msg.Opcode == 1 {
				add(WhereWS, 0, string(msg.Data))
			}
		}
		if len(r.Hits) > 0 {
			emit(r)
		}
	}
	return nil
}
