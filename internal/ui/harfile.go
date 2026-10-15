package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/config"
	"nlrcapture/internal/proxy"
	"nlrcapture/internal/winutil"
)

var harFilters = []winutil.Filter{{Name: "HAR 文件", Pattern: "*.har"}, {Name: "JSON", Pattern: "*.json"}, {Name: "所有文件", Pattern: "*.*"}}

func regexpQuote(s string) string { return regexp.QuoteMeta(s) }

// pickImport asks for a HAR file and imports it.
func (a *App) pickImport() {
	hwnd := a.host.HWND()
	go func() {
		p, err := winutil.OpenFile(hwnd, "导入 HAR", harFilters)
		if err != nil {
			return
		}
		a.Post(func() { a.importHAR(p) })
	}()
}

// importHAR reads a HAR file in the background and adds its entries.
func (a *App) importHAR(p string) {
	go func() {
		f, err := os.Open(p)
		var ss []*capture.Session
		if err == nil {
			ss, err = capture.ReadHAR(bufio.NewReaderSize(f, 1<<20))
			f.Close()
		}
		a.Post(func() {
			if err != nil {
				a.Toast(toastError, "导入失败："+err.Error())
				return
			}
			a.store.AddAll(ss)
			a.Toast(toastOK, fmt.Sprintf("已导入 %d 个请求", len(ss)))
		})
	}()
}

// pickExport saves all sessions, or the ones the filter shows, as HAR.
func (a *App) pickExport(all bool) {
	var ss []*capture.Session
	if all {
		ss = a.store.All()
	} else {
		a.list.refresh()
		ss = append(ss, a.list.view...)
	}
	if len(ss) == 0 {
		a.Toast(toastInfo, "没有请求")
		return
	}
	a.exportSessions(ss, "capture-"+time.Now().Format("20060102-150405")+".har")
}

func (a *App) exportSessions(ss []*capture.Session, name string) {
	hwnd := a.host.HWND()
	go func() {
		p, err := winutil.SaveFile(hwnd, "导出 HAR", name, "har", harFilters)
		if err != nil {
			return
		}
		f, err := os.Create(p)
		if err == nil {
			w := bufio.NewWriterSize(f, 1<<20)
			err = capture.WriteHAR(w, ss, Version)
			if err == nil {
				err = w.Flush()
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		a.Post(func() {
			if err != nil {
				a.Toast(toastError, "导出失败："+err.Error())
			} else {
				a.Toast(toastOK, fmt.Sprintf("已导出 %d 个请求", len(ss)))
			}
		})
	}()
}

// copyHAR puts the sessions on the clipboard as HAR.
func (a *App) copyHAR(ss []*capture.Session) {
	var buf bytes.Buffer
	if err := capture.WriteHAR(&buf, ss, Version); err != nil {
		a.Toast(toastError, err.Error())
		return
	}
	a.copyText(buf.String(), " HAR")
}

// addRule appends an AutoResponder rule and applies it.
func (a *App) addRule(r proxy.Rule, msg string) {
	a.updateSettings(func(s *config.Settings) {
		r.ID = fmt.Sprint(len(s.Rules) + 1)
		s.Rules = append(s.Rules, r)
	})
	a.svc.applyConfig()
	a.Toast(toastOK, msg)
}

var _ = strings.TrimSpace
