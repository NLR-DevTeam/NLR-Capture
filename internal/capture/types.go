package capture

import (
	"path"
	"strings"
)

// Resource types, as the DevTools type filter groups them.
const (
	TypeDocument = "document"
	// TypeAPI covers fetch and XMLHttpRequest calls alike.
	TypeAPI        = "api"
	TypeScript     = "script"
	TypeStylesheet = "stylesheet"
	TypeImage      = "image"
	TypeMedia      = "media"
	TypeFont       = "font"
	TypeManifest   = "manifest"
	TypeWebSocket  = "websocket"
	TypeWasm       = "wasm"
	TypeTunnel     = "tunnel"
	TypeOther      = "other"
)

// ResourceType classifies a session like the DevTools resource types, from
// the request's fetch metadata, the response type and the URL extension.
func ResourceType(s *Session) string {
	if s.Kind == KindTunnel {
		return TypeTunnel
	}
	if s.Kind == KindWebSocket || strings.EqualFold(s.ReqHeader.Get("Upgrade"), "websocket") {
		return TypeWebSocket
	}
	switch strings.ToLower(s.ReqHeader.Get("Sec-Fetch-Dest")) {
	case "document", "iframe", "frame", "embed", "object":
		return TypeDocument
	case "script", "worker", "sharedworker", "serviceworker":
		return TypeScript
	case "style":
		return TypeStylesheet
	case "image":
		return TypeImage
	case "audio", "video", "track":
		return TypeMedia
	case "font":
		return TypeFont
	case "manifest":
		return TypeManifest
	case "empty":
		if s.ReqHeader.Get("X-Requested-With") != "" {
			return TypeAPI
		}
		if t := typeOfMedia(s.ContentType()); t != "" && t != TypeDocument && t != TypeScript && t != TypeStylesheet {
			return t
		}
		return TypeAPI
	}
	if s.ReqHeader.Get("X-Requested-With") != "" {
		return TypeAPI
	}
	if t := typeOfMedia(s.ContentType()); t != "" {
		if t == TypeDocument && !strings.Contains(s.ReqHeader.Get("Accept"), "text/html") {
			// HTML fetched by a program rather than navigated to.
			return TypeAPI
		}
		return t
	}
	if t := typeOfExt(s.ParsedURL().Path); t != "" {
		return t
	}
	ct := s.ContentType()
	if strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "protobuf") || strings.Contains(ct, "grpc") {
		return TypeAPI
	}
	return TypeOther
}

func typeOfMedia(mt string) string {
	switch {
	case mt == "":
		return ""
	case mt == "text/html", mt == "application/xhtml+xml":
		return TypeDocument
	case strings.Contains(mt, "javascript"), strings.Contains(mt, "ecmascript"):
		return TypeScript
	case mt == "text/css":
		return TypeStylesheet
	case strings.HasPrefix(mt, "image/"):
		return TypeImage
	case strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"), mt == "application/vnd.apple.mpegurl", mt == "application/dash+xml":
		return TypeMedia
	case strings.HasPrefix(mt, "font/"), strings.Contains(mt, "font-woff"), strings.Contains(mt, "x-font"):
		return TypeFont
	case mt == "application/manifest+json":
		return TypeManifest
	case mt == "application/wasm":
		return TypeWasm
	case strings.Contains(mt, "json"), strings.Contains(mt, "xml"), strings.Contains(mt, "protobuf"), strings.Contains(mt, "grpc"), mt == "text/event-stream":
		return TypeAPI
	}
	return ""
}

func typeOfExt(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm":
		return TypeDocument
	case ".js", ".mjs":
		return TypeScript
	case ".css":
		return TypeStylesheet
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".bmp", ".avif":
		return TypeImage
	case ".mp4", ".webm", ".mp3", ".m4a", ".ogg", ".wav", ".m3u8", ".ts", ".flv":
		return TypeMedia
	case ".woff", ".woff2", ".ttf", ".otf", ".eot":
		return TypeFont
	case ".wasm":
		return TypeWasm
	case ".webmanifest":
		return TypeManifest
	}
	return ""
}

// TypeLabel returns the text of the Type column: the resource type, or for
// images the image format, as DevTools shows it.
func TypeLabel(s *Session) string {
	t := ResourceType(s)
	switch t {
	case TypeImage, TypeFont, TypeMedia:
		mt := s.ContentType()
		if i := strings.IndexByte(mt, '/'); i >= 0 && mt[i+1:] != "" {
			sub := mt[i+1:]
			sub = strings.TrimPrefix(sub, "x-")
			if sub == "vnd.microsoft.icon" {
				sub = "x-icon"
			}
			return sub
		}
	case TypeTunnel:
		return "tunnel"
	}
	return t
}

// StatusText returns the reason phrase for a status code.
func StatusText(code int) string {
	if t, ok := statusText[code]; ok {
		return t
	}
	return ""
}

var statusText = map[int]string{
	100: "Continue", 101: "Switching Protocols", 102: "Processing", 103: "Early Hints",
	200: "OK", 201: "Created", 202: "Accepted", 203: "Non-Authoritative Information",
	204: "No Content", 205: "Reset Content", 206: "Partial Content", 207: "Multi-Status",
	300: "Multiple Choices", 301: "Moved Permanently", 302: "Found", 303: "See Other",
	304: "Not Modified", 307: "Temporary Redirect", 308: "Permanent Redirect",
	400: "Bad Request", 401: "Unauthorized", 402: "Payment Required", 403: "Forbidden",
	404: "Not Found", 405: "Method Not Allowed", 406: "Not Acceptable",
	407: "Proxy Authentication Required", 408: "Request Timeout", 409: "Conflict",
	410: "Gone", 411: "Length Required", 412: "Precondition Failed",
	413: "Content Too Large", 414: "URI Too Long", 415: "Unsupported Media Type",
	416: "Range Not Satisfiable", 417: "Expectation Failed", 418: "I'm a teapot",
	421: "Misdirected Request", 422: "Unprocessable Content", 425: "Too Early",
	426: "Upgrade Required", 428: "Precondition Required", 429: "Too Many Requests",
	431: "Request Header Fields Too Large", 451: "Unavailable For Legal Reasons",
	500: "Internal Server Error", 501: "Not Implemented", 502: "Bad Gateway",
	503: "Service Unavailable", 504: "Gateway Timeout", 505: "HTTP Version Not Supported",
	507: "Insufficient Storage", 511: "Network Authentication Required",
}
