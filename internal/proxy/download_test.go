package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"nlrcapture/internal/capture"
)

func TestDownload(t *testing.T) {
	e, _, _ := setup(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" || r.Header.Get("If-None-Match") != "" {
			http.Error(w, "partial header leaked", 400)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		zw.Write(data)
		zw.Close()
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, p := range []string{"/file", "/gz", "/redir"} {
		s := &capture.Session{Kind: capture.KindHTTP, Method: "GET", URL: srv.URL + p,
			ReqHeader: capture.Header{{Name: "Range", Value: "bytes=0-99"}, {Name: "If-None-Match", Value: `"x"`}, {Name: "Accept-Encoding", Value: "gzip"}}}
		var buf bytes.Buffer
		var last int64
		err := e.Download(context.Background(), s, &buf, func(done, total int64) { last = done })
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(buf.Bytes(), data) || last != int64(len(data)) {
			t.Fatalf("%s: got %d bytes, progress %d", p, buf.Len(), last)
		}
	}
}
