package proxy

import (
	"html"
	"strings"

	"nlrcapture/internal/capture"
)

// servePage answers requests addressed to the proxy itself: a page that
// explains the proxy and offers the root certificate for download, which
// is how phones and other machines get it.
func (h *h1conn) servePage(hd *head, method, path string) bool {
	fr, err := requestFraming(hd.header)
	if err == nil && (fr.length > 0 || fr.chunked) {
		// Bodies are not expected here; drop the connection after.
		writeSimple(h.bw, 405, nil, []byte("Method not allowed\n"), true)
		return false
	}
	p := path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	closeConn := hasToken(hd.header, "Connection", "close")
	ca := h.e.CA
	switch {
	case ca != nil && (p == "/cert" || p == "/cert.crt" || p == "/NLRCaptureRoot.crt" || p == "/cert.pem"):
		hdr := capture.Header{
			{Name: "Content-Type", Value: "application/x-x509-ca-cert"},
			{Name: "Content-Disposition", Value: `attachment; filename="NLRCaptureRoot.crt"`},
		}
		writeSimple(h.bw, 200, hdr, ca.CertPEM, closeConn)
	case ca != nil && (p == "/cert.cer" || p == "/cert.der"):
		hdr := capture.Header{
			{Name: "Content-Type", Value: "application/pkix-cert"},
			{Name: "Content-Disposition", Value: `attachment; filename="NLRCaptureRoot.cer"`},
		}
		writeSimple(h.bw, 200, hdr, ca.DER(), closeConn)
	case p == "/" || p == "/index.html":
		fp := ""
		if ca != nil {
			fp = ca.Fingerprint()
		}
		body := strings.ReplaceAll(pageHTML, "{{fp}}", html.EscapeString(fp))
		body = strings.ReplaceAll(body, "{{ver}}", html.EscapeString(h.e.Version))
		hdr := capture.Header{{Name: "Content-Type", Value: "text/html; charset=utf-8"}, {Name: "Cache-Control", Value: "no-store"}}
		if method == "HEAD" {
			writeSimple(h.bw, 200, hdr, nil, closeConn)
		} else {
			writeSimple(h.bw, 200, hdr, []byte(body), closeConn)
		}
	default:
		writeSimple(h.bw, 404, nil, []byte("Not found\n"), closeConn)
	}
	return !closeConn
}

const pageHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>NLR Capture</title>
<style>
body{font:15px/1.6 "Microsoft YaHei UI","PingFang SC",system-ui,sans-serif;margin:0;background:#0b0d10;color:#d9dee6}
main{max-width:640px;margin:0 auto;padding:40px 20px}
h1{font-size:24px;margin:0 0 24px}
a.btn{display:inline-block;background:#00b935;color:#fff;text-decoration:none;padding:10px 18px;border-radius:8px;margin:0 10px 10px 0}
a.alt{background:#1e242c;border:1px solid #34404d}
code{font-family:"Cascadia Mono",Consolas,monospace;font-size:12px;word-break:break-all;color:#8d97a6}
@media (prefers-color-scheme:light){body{background:#fff;color:#1c2128}code{color:#555f6b}a.alt{background:#f3f4f6;border-color:#d0d5db;color:#1c2128}}
</style></head><body><main>
<h1>NLR Capture</h1>
<p>要解密 HTTPS 流量，请在设备上安装并信任此根证书：</p>
<p><a class="btn" href="/cert.crt">下载根证书</a><a class="btn alt" href="/cert.cer">DER 格式</a></p>
<p>SHA-256 指纹：<br><code>{{fp}}</code></p>
</main></body></html>
`
