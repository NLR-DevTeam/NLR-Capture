package ui

import (
	"path/filepath"
	"strings"
)

// A rule's action is stored as one string in the proxy's syntax (see
// proxy.Rule); the rules dialog edits it as a kind and a parameter.

type actionKind struct {
	id, label, hint string
	// param reports whether the kind takes a parameter.
	param bool
}

var actionKinds = []actionKind{
	{"status", "返回状态码", "例如 404", true},
	{"body", "返回文本", "响应正文", true},
	{"file", "返回文件", "文件路径", true},
	{"block", "屏蔽", "", false},
	{"drop", "断开连接", "", false},
	{"redir", "重定向", "目标网址", true},
	{"url", "转发到网址", "目标网址，可用 $1 引用正则分组", true},
	{"delay", "延迟", "毫秒", true},
	{"header", "设置请求标头", "名称=值", true},
	{"resp-header", "设置响应标头", "名称=值", true},
}

func actionKindOf(id string) actionKind {
	for _, k := range actionKinds {
		if k.id == id {
			return k
		}
	}
	return actionKinds[0]
}

func actionOptions() [][2]string {
	out := make([][2]string, len(actionKinds))
	for i, k := range actionKinds {
		out[i] = [2]string{k.id, k.label}
	}
	return out
}

// splitAction turns an action string into its kind and parameter.
func splitAction(a string) (kind, param string) {
	a = strings.TrimSpace(a)
	low := strings.ToLower(a)
	cut := func(p string) string { return strings.TrimSpace(a[len(p):]) }
	switch {
	case a == "":
		return "status", "404"
	case low == "*block":
		return "block", ""
	case low == "*drop", low == "*reset":
		return "drop", ""
	case strings.HasPrefix(low, "*delay:"):
		return "delay", cut("*delay:")
	case strings.HasPrefix(low, "*redir:"):
		return "redir", cut("*redir:")
	case strings.HasPrefix(low, "*header:"):
		return "header", cut("*header:")
	case strings.HasPrefix(low, "*resp-header:"):
		return "resp-header", cut("*resp-header:")
	case strings.HasPrefix(low, "*body:"):
		return "body", a[len("*body:"):]
	case len(a) > 1 && a[0] == '*' && isDigitsUI(a[1:]):
		return "status", a[1:]
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
		return "url", a
	}
	return "file", a
}

// joinAction builds the action string for a kind and parameter.
func joinAction(kind, param string) string {
	p := strings.TrimSpace(param)
	switch kind {
	case "status":
		return "*" + p
	case "block":
		return "*block"
	case "drop":
		return "*drop"
	case "body":
		return "*body:" + param
	case "url", "file":
		return p
	}
	return "*" + kind + ":" + p
}

// actionSummary describes an action for the rule list.
func actionSummary(a string) string {
	kind, p := splitAction(a)
	switch kind {
	case "status":
		return "返回 " + p
	case "block", "drop":
		return actionKindOf(kind).label
	case "body":
		return "返回文本"
	case "file":
		return "返回文件 " + filepath.Base(p)
	case "redir":
		return "重定向到 " + p
	case "url":
		return "转发到 " + p
	case "delay":
		return "延迟 " + p + " 毫秒"
	case "header":
		return "请求标头 " + p
	case "resp-header":
		return "响应标头 " + p
	}
	return a
}

// actionError says what is wrong with a parameter, or "" if it is fine.
func actionError(kind, param string) string {
	p := strings.TrimSpace(param)
	switch kind {
	case "status":
		if len(p) != 3 || !isDigitsUI(p) || p[0] < '1' || p[0] > '5' {
			return "状态码应为 100 到 599"
		}
	case "delay":
		if !isDigitsUI(p) {
			return "延迟应为毫秒数"
		}
	case "header", "resp-header":
		if n, _, ok := strings.Cut(p, "="); !ok || strings.TrimSpace(n) == "" {
			return "标头应写成 名称=值"
		}
	case "redir", "url", "file":
		if p == "" {
			return "请填写" + actionKindOf(kind).hint
		}
	}
	return ""
}

func isDigitsUI(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// methodOptions are the choices for a rule's method; "" is any.
var methodOptions = [][2]string{
	{"", "全部"}, {"GET", "GET"}, {"POST", "POST"}, {"PUT", "PUT"}, {"PATCH", "PATCH"},
	{"DELETE", "DELETE"}, {"HEAD", "HEAD"}, {"OPTIONS", "OPTIONS"},
}
