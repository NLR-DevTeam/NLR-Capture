// Command shot drives the NLR Capture UI off-screen, sends real traffic
// through its proxy to local test servers, and writes PNG screenshots. It
// is a development tool for checking layout and drawing without a window.
// It never touches the system proxy: the app runs in the manual mode.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"golang.org/x/net/websocket"

	"nlrcapture/internal/certs"
	"nlrcapture/internal/config"
	"nlrcapture/internal/ui"
)

type host struct{}

func (h *host) Invalidate()                    {}
func (h *host) Perform(a system.Action)        {}
func (h *host) HWND() uintptr                  { return 0 }
func (h *host) SetWindowMode(m app.WindowMode) {}

type driver struct {
	a      *ui.App
	router *input.Router
	ops    op.Ops
	win    *headless.Window
	size   image.Point
	scale  float32
	dir    string
	start  time.Time
}

func (d *driver) frame() {
	d.ops.Reset()
	gtx := layout.Context{
		Ops:         &d.ops,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: d.scale, PxPerSp: d.scale},
		Constraints: layout.Exact(d.size),
		Source:      d.router.Source(),
	}
	d.a.Layout(gtx)
	d.router.Frame(gtx.Ops)
}

func (d *driver) run(dur time.Duration) {
	end := time.Now().Add(dur)
	for {
		d.frame()
		if time.Now().After(end) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *driver) shot(name string) {
	d.frame()
	d.frame()
	if err := d.win.Frame(&d.ops); err != nil {
		fmt.Fprintln(os.Stderr, "frame:", err)
		os.Exit(1)
	}
	img := image.NewRGBA(image.Rectangle{Max: d.size})
	if err := d.win.Screenshot(img); err != nil {
		fmt.Fprintln(os.Stderr, "screenshot:", err)
		os.Exit(1)
	}
	f, err := os.Create(filepath.Join(d.dir, name+".png"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	png.Encode(f, img)
	f.Close()
	fmt.Printf("%-14s %s\n", name, time.Since(d.start).Round(time.Millisecond))
}

func (d *driver) px(dp float32) float32 { return dp * d.scale }

func (d *driver) move(x, y float32) {
	d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y)})
	d.frame()
}

func (d *driver) click(x, y float32, btn pointer.Buttons) {
	p := f32.Pt(x, y)
	d.move(x, y)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: btn, Position: p})
	d.frame()
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	d.frame()
	d.frame()
}

func (d *driver) key(name key.Name, mods key.Modifiers) {
	d.router.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press})
	d.frame()
	d.router.Queue(key.Event{Name: name, Modifiers: mods, State: key.Release})
	d.frame()
}

// ---- Test traffic -----------------------------------------------------

func pngBytes() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 3), B: 180, A: 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func handler() http.Handler {
	mux := http.NewServeMux()
	logo := pngBytes()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Set-Cookie", "session=abc123; Path=/; HttpOnly; Secure; SameSite=Lax")
		fmt.Fprint(w, "<!doctype html><html><head><title>Demo</title><link rel=stylesheet href=/static/app.css></head><body><h1>你好，NLR Capture</h1><script src=/static/app.js></script></body></html>")
	})
	mux.HandleFunc("/static/app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body{font-family:sans-serif;margin:0}\nh1{color:#00b935}\n")
	})
	mux.HandleFunc("/static/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		fmt.Fprint(zw, strings.Repeat("console.log('hello from app.js');\n", 40))
		zw.Close()
	})
	mux.HandleFunc("/img/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(logo)
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, `{"id":1024,"name":"张三","email":"zhangsan@example.com","roles":["admin","dev"],"profile":{"city":"上海","age":28,"verified":true,"tags":null},"projects":[{"id":1,"title":"NLR Shell"},{"id":2,"title":"NLR Capture"}]}`)
	})
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"received":%q}`, string(b))
	})
	mux.HandleFunc("/api/missing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, 404)
	})
	mux.HandleFunc("/api/error", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", 500)
	})
	mux.HandleFunc("/fonts/inter.woff2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		w.Write(bytes.Repeat([]byte{0x77, 0x4f, 0x46, 0x32, 0, 1, 2, 3}, 600))
	})
	mux.Handle("/ws", websocket.Handler(func(c *websocket.Conn) {
		buf := make([]byte, 1024)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			c.Write([]byte(fmt.Sprintf(`{"echo":%q,"t":%d}`, buf[:n], time.Now().Unix())))
		}
	}))
	return mux
}

// wsTalk opens a WebSocket through the proxy and exchanges messages.
func wsTalk(proxyAddr, target string) error {
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	u, _ := url.Parse(target)
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: http://%s\r\n\r\n", target, u.Host, u.Host)
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "101") {
		return fmt.Errorf("no upgrade: %s", line)
	}
	for {
		l, _ := br.ReadString('\n')
		if l == "\r\n" || l == "" {
			break
		}
	}
	for _, msg := range []string{`{"type":"hello","user":"张三"}`, `{"type":"ping","seq":1}`, `{"type":"subscribe","topic":"orders"}`} {
		p := []byte(msg)
		frame := []byte{0x81, 0x80 | byte(len(p)), 1, 2, 3, 4}
		for i := range p {
			frame = append(frame, p[i]^[]byte{1, 2, 3, 4}[i%4])
		}
		c.Write(frame)
		hdr := make([]byte, 2)
		io.ReadFull(br, hdr)
		n := int(hdr[1] & 0x7f)
		if n == 126 {
			ext := make([]byte, 2)
			io.ReadFull(br, ext)
			n = int(binary.BigEndian.Uint16(ext))
		}
		io.ReadFull(br, make([]byte, n))
	}
	return nil
}

func traffic(dataDir string, port int) {
	plain := httptest.NewServer(handler())
	tlsSrv := httptest.NewUnstartedServer(handler())
	tlsSrv.EnableHTTP2 = true
	tlsSrv.StartTLS()
	ca, err := certs.Load(filepath.Join(dataDir, "certs"))
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	pu, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	get := func(u string, h map[string]string) {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) NLRShot/1.0")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	base := strings.Replace(tlsSrv.URL, "127.0.0.1", "localhost", 1)
	get(base+"/", map[string]string{"Accept": "text/html", "Sec-Fetch-Dest": "document"})
	get(base+"/static/app.css", map[string]string{"Sec-Fetch-Dest": "style"})
	get(base+"/static/app.js", map[string]string{"Sec-Fetch-Dest": "script", "Accept-Encoding": "gzip"})
	get(base+"/img/logo.png", map[string]string{"Sec-Fetch-Dest": "image"})
	get(base+"/fonts/inter.woff2", map[string]string{"Sec-Fetch-Dest": "font"})
	get(base+"/api/user?id=1024&fields=name,email", map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "application/json", "Cookie": "session=abc123; theme=dark"})
	req, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"username":"zhangsan","password":"******","remember":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	if resp, err := c.Do(req); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	get(plain.URL+"/api/missing", map[string]string{"X-Requested-With": "XMLHttpRequest"})
	get(plain.URL+"/api/error", nil)
	get("http://127.0.0.1:1/unreachable", nil)
	wsTalk(fmt.Sprintf("127.0.0.1:%d", port), "ws"+strings.TrimPrefix(plain.URL, "http")+"/ws")
}

func main() {
	out := flag.String("o", "shots", "output directory")
	w := flag.Int("w", 1360, "width in dp")
	h := flag.Int("h", 820, "height in dp")
	scale := flag.Float64("scale", 1, "pixels per dp")
	only := flag.String("only", "", "comma separated list of shots to take (default all)")
	flag.Parse()
	want := func(name string) bool {
		return *only == "" || strings.Contains(","+*only+",", ","+name+",")
	}

	dir, _ := os.MkdirTemp("", "nlrcapshot")
	defer os.RemoveAll(dir)
	cfg := config.Open(dir)
	set := config.Defaults()
	set.Mode = config.ModeManual
	set.Animations = false
	set.CertAsked = true
	set.Appearance = config.AppearanceDark
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	set.Port = l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg.Save(set)

	size := image.Pt(int(float64(*w)**scale), int(float64(*h)**scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		fmt.Fprintln(os.Stderr, "headless:", err)
		os.Exit(1)
	}
	defer win.Release()
	a := ui.New(&host{}, cfg, dir)
	a.SetDecorated(false)
	d := &driver{a: a, router: new(input.Router), win: win, size: size, scale: float32(*scale), dir: *out, start: time.Now()}
	os.MkdirAll(*out, 0o755)
	a.Start()
	d.run(100 * time.Millisecond)
	if want("empty") {
		d.shot("empty")
	}
	traffic(dir, set.Port)
	d.run(600 * time.Millisecond)
	fmt.Println("captured", a.ListLen(), "requests")
	if want("list") {
		d.shot("list")
	}
	if want("headers") {
		a.SelectURL("/api/user")
		a.SetTab("headers")
		d.shot("headers")
	}
	if want("preview") {
		a.SelectURL("/api/user")
		a.SetTab("preview")
		d.shot("preview")
	}
	if want("response") {
		a.SelectURL("/static/app.js")
		a.SetTab("response")
		d.shot("response")
	}
	if want("payload") {
		a.SelectURL("/api/login")
		a.SetTab("payload")
		d.shot("payload")
	}
	if want("timing") {
		a.SelectURL("/api/user")
		a.SetTab("timing")
		d.shot("timing")
	}
	if want("cookies") {
		a.SelectURL("/api/user")
		a.SetTab("cookies")
		d.shot("cookies")
	}
	if want("image") {
		a.SelectURL("/img/logo.png")
		a.SetTab("preview")
		d.shot("image")
	}
	if want("ws") {
		a.SelectURL("/ws")
		a.SetTab("messages")
		d.shot("ws")
	}
	if want("raw") {
		a.SelectURL("/api/login")
		a.SetTab("raw")
		d.shot("raw")
	}
	if want("menu") {
		a.SelectURL("/api/user")
		a.RowMenu()
		d.shot("menu")
		a.CloseDialogs()
	}
	a.ClearSelection()
	if want("filter") {
		a.SetFilter("status-code:4xx")
		d.shot("filter")
		a.SetFilter("")
		a.SetTypes("api")
		d.shot("filter-type")
		a.SetTypes()
	}
	if want("search") {
		a.Search("张三")
		for i := 0; i < 50 && !a.SearchDone(); i++ {
			d.run(50 * time.Millisecond)
		}
		d.run(100 * time.Millisecond)
		d.shot("search")
		a.Search("")
		d.key(key.NameEscape, 0)
	}
	for _, name := range []string{"settings", "cert", "rules", "breakpoints", "compose", "about"} {
		if want(name) {
			if name == "compose" {
				a.SelectURL("/api/login")
			}
			a.OpenDialog(name)
			d.run(100 * time.Millisecond)
			d.shot(name)
			a.CloseDialogs()
			a.ClearSelection()
		}
	}
	if want("light") {
		a.SetLight(true)
		a.SelectURL("/api/user")
		a.SetTab("headers")
		d.shot("light")
		a.ClearSelection()
		d.shot("light-list")
		a.SetLight(false)
	}
	if want("click") {
		// Real pointer input: click the third row, then a detail tab.
		d.click(d.px(200), d.px(40+34+34+26+22*2+11), pointer.ButtonPrimary)
		d.shot("click")
	}
	a.Shutdown()
}
