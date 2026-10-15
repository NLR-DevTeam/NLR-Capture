package proxy

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/certs"
)

func setup(t *testing.T) (*Engine, *http.Client, *http.Client) {
	t.Helper()
	ca, err := certs.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := capture.NewStore()
	e := New(st, ca)
	c := e.Config()
	c.IgnoreCertErrors = true
	c.AutoBypass = false
	e.SetConfig(c)
	l, err := e.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	pu, _ := url.Parse("http://" + l.Addr().String())
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	tr1 := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, DisableCompression: true}
	tr2 := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true, DisableCompression: true}
	return e, &http.Client{Transport: tr1, Timeout: 20 * time.Second}, &http.Client{Transport: tr2, Timeout: 20 * time.Second}
}

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Proto", r.Proto)
		fmt.Fprintf(w, "hello %s", r.Method)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		for i := range 3 {
			fmt.Fprintf(w, "part%d;", i)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		zw := gzip.NewWriter(w)
		zw.Write([]byte("<html>压缩内容</html>"))
		zw.Close()
	})
	return mux
}

func waitDone(t *testing.T, st *capture.Store, n int) []*capture.Session {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		all := st.All()
		done := 0
		for _, s := range all {
			if !s.Running() {
				done++
			}
		}
		if done >= n {
			return all
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, s := range st.All() {
		t.Logf("session %d %s %s state=%d err=%s", s.ID, s.Method, s.URL, s.State, s.Error)
	}
	t.Fatalf("timed out waiting for %d sessions", n)
	return nil
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestHTTP(t *testing.T) {
	e, c, _ := setup(t)
	srv := httptest.NewServer(handler())
	defer srv.Close()
	_, body := get(t, c, srv.URL+"/hello")
	if body != "hello GET" {
		t.Fatalf("body %q", body)
	}
	resp, err := c.Post(srv.URL+"/echo", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `{"a":1}` {
		t.Fatalf("echo %q", b)
	}
	_, body = get(t, c, srv.URL+"/chunked")
	if body != "part0;part1;part2;" {
		t.Fatalf("chunked %q", body)
	}
	_, body = get(t, c, srv.URL+"/gzip")
	all := waitDone(t, e.Store, 4)
	if len(all) != 4 {
		t.Fatalf("%d sessions", len(all))
	}
	if all[1].Method != "POST" || string(all[1].ReqBody) != `{"a":1}` || string(all[1].RespBody) != `{"a":1}` {
		t.Fatalf("post session %+v", all[1])
	}
	if string(all[2].RespBody) != "part0;part1;part2;" {
		t.Fatalf("chunked capture %q", all[2].RespBody)
	}
	txt, ok, err := capture.BodyText(all[3], true)
	if !ok || err != nil || txt != "<html>压缩内容</html>" {
		t.Fatalf("gzip body %q %v %v", txt, ok, err)
	}
	if all[0].Status != 200 || all[0].Timings.Wait < 0 {
		t.Fatalf("timings %+v", all[0].Timings)
	}
}

func TestHTTPS(t *testing.T) {
	e, c1, c2 := setup(t)
	srv := httptest.NewUnstartedServer(handler())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	resp, body := get(t, c1, srv.URL+"/hello")
	if body != "hello GET" || resp.Proto != "HTTP/1.1" {
		t.Fatalf("h1: %q %s", body, resp.Proto)
	}
	resp, body = get(t, c2, srv.URL+"/hello")
	if body != "hello GET" || resp.Proto != "HTTP/2.0" || resp.Header.Get("X-Proto") != "HTTP/2.0" {
		t.Fatalf("h2: %q %s upstream %s", body, resp.Proto, resp.Header.Get("X-Proto"))
	}
	resp2, err := c2.Post(srv.URL+"/echo", "application/json", bytes.NewReader([]byte(`[1,2,3]`)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(b) != "[1,2,3]" {
		t.Fatalf("h2 echo %q", b)
	}
	all := waitDone(t, e.Store, 3)
	for _, s := range all {
		if !s.Decrypted || s.Scheme != "https" || s.Status != 200 {
			t.Fatalf("session %+v", s)
		}
	}
	if all[1].Proto != "h2" || all[1].ReqHeader.Get(":authority") == "" {
		t.Fatalf("h2 session proto %s headers %v", all[1].Proto, all[1].ReqHeader)
	}
	if string(all[2].ReqBody) != "[1,2,3]" || string(all[2].RespBody) != "[1,2,3]" {
		t.Fatalf("h2 bodies %q %q", all[2].ReqBody, all[2].RespBody)
	}
	if all[0].TLS == nil || all[0].TLS.Version == "" {
		t.Fatalf("tls info %+v", all[0].TLS)
	}
}

func TestRules(t *testing.T) {
	e, c, c2 := setup(t)
	srv := httptest.NewServer(handler())
	defer srv.Close()
	tsrv := httptest.NewUnstartedServer(handler())
	tsrv.EnableHTTP2 = true
	tsrv.StartTLS()
	defer tsrv.Close()
	cfg := e.Config()
	cfg.Rules = []Rule{
		{Enabled: true, Match: "/missing", Action: "*404"},
		{Enabled: true, Match: "regex:/mock$", Action: `*body:{"mocked":true}`},
		{Enabled: true, Match: "/blocked", Action: "*block"},
	}
	e.SetConfig(cfg)
	resp, _ := get(t, c, srv.URL+"/missing")
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, body := get(t, c, srv.URL+"/mock")
	if body != `{"mocked":true}` {
		t.Fatalf("mock %q", body)
	}
	_, body = get(t, c2, tsrv.URL+"/mock")
	if body != `{"mocked":true}` {
		t.Fatalf("h2 mock %q", body)
	}
	resp, _ = get(t, c, srv.URL+"/blocked")
	if resp.StatusCode != 403 {
		t.Fatalf("blocked %d", resp.StatusCode)
	}
	all := waitDone(t, e.Store, 4)
	if all[3].State != capture.StateBlocked || all[0].Rule == "" {
		t.Fatalf("rule sessions %+v %+v", all[0], all[3])
	}
}

func TestBreakpoint(t *testing.T) {
	e, c, _ := setup(t)
	srv := httptest.NewServer(handler())
	defer srv.Close()
	cfg := e.Config()
	cfg.Breakpoints = Breakpoints{Request: true, Match: "/echo"}
	e.SetConfig(cfg)
	paused := make(chan int64, 1)
	e.OnPause = func(id int64) { paused <- id }
	go func() {
		id := <-paused
		s := e.Store.Get(id)
		raw := BreakText(s)
		raw = strings.Replace(raw, `"orig"`, `"edited"`, 1)
		e.Continue(id, Resume{Raw: raw, Edit: true})
	}()
	resp, err := c.Post(srv.URL+"/echo", "application/json", strings.NewReader(`"orig"`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `"edited"` {
		t.Fatalf("edited body %q", b)
	}
}

func TestComposeAndHAR(t *testing.T) {
	e, _, _ := setup(t)
	srv := httptest.NewServer(handler())
	defer srv.Close()
	e.Send("POST", srv.URL+"/echo", capture.Header{{Name: "Content-Type", Value: "application/json"}}, []byte(`{"x":"y"}`))
	all := waitDone(t, e.Store, 1)
	if string(all[0].RespBody) != `{"x":"y"}` {
		t.Fatalf("compose %q err %s", all[0].RespBody, all[0].Error)
	}
	var buf bytes.Buffer
	if err := capture.WriteHAR(&buf, all, "test"); err != nil {
		t.Fatal(err)
	}
	back, err := capture.ReadHAR(&buf)
	if err != nil || len(back) != 1 {
		t.Fatalf("read har %v %d", err, len(back))
	}
	if back[0].URL != all[0].URL || string(back[0].RespBody) != `{"x":"y"}` || back[0].Status != 200 {
		t.Fatalf("round trip %+v", back[0])
	}
}

func TestLocalPage(t *testing.T) {
	e, c, _ := setup(t)
	_ = e
	_, body := get(t, c, "http://nlr.capture/")
	if !strings.Contains(body, "NLR Capture") {
		t.Fatalf("page %q", body)
	}
	_, body = get(t, c, "http://nlr.capture/cert.crt")
	if !strings.Contains(body, "BEGIN CERTIFICATE") {
		t.Fatalf("cert %q", body)
	}
}

func TestTunnelPassthrough(t *testing.T) {
	e, c, _ := setup(t)
	cfg := e.Config()
	cfg.Decrypt = false
	e.SetConfig(cfg)
	srv := httptest.NewTLSServer(handler())
	defer srv.Close()
	// Without interception the client sees the server's own certificate.
	tr := c.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	c2 := &http.Client{Transport: tr}
	_, body := get(t, c2, srv.URL+"/hello")
	if body != "hello GET" {
		t.Fatalf("tunnel body %q", body)
	}
	tr.CloseIdleConnections()
	all := waitDone(t, e.Store, 1)
	if all[0].Kind != capture.KindTunnel || all[0].BytesDown == 0 {
		t.Fatalf("tunnel %+v", all[0])
	}
}

var _ = net.JoinHostPort
