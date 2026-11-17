package ui

import (
	"bufio"
	"context"
	"errors"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/osutil"
)

// downloadName proposes a file name for the session's response body.
func downloadName(s *capture.Session) string {
	name := ""
	if cd := s.RespHeader.Get("Content-Disposition"); cd != "" {
		if _, p, err := mime.ParseMediaType(cd); err == nil {
			name = p["filename"]
		}
	}
	if name == "" {
		if u, err := url.Parse(s.URL); err == nil {
			name = path.Base(u.Path)
		}
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == "_" {
		name = "download"
		if exts, _ := mime.ExtensionsByType(s.RespHeader.Get("Content-Type")); len(exts) > 0 {
			name += exts[0]
		}
	}
	return name
}

// downloadTo asks where to save the full response of s, then downloads it
// with a progress dialog.
func (a *App) downloadTo(s *capture.Session) {
	name := downloadName(s)
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	filters := []osutil.Filter{{Name: "所有文件", Pattern: "*.*"}}
	if ext != "" {
		filters = append([]osutil.Filter{{Name: strings.ToUpper(ext) + " 文件", Pattern: "*." + ext}}, filters...)
	}
	a.choosePath(true, "下载到", name, ext, filters, func(p string) {
		a.Post(func() {
			d := &downloadDialog{path: p, url: s.URL}
			d.total.Store(-1)
			a.Open(d)
			d.start(a, s)
		})
	})
}

type downloadDialog struct {
	path, url string
	started   time.Time
	cancel    context.CancelFunc

	done, total atomic.Int64
	lastInv     atomic.Int64
	mu          sync.Mutex
	finished    bool
	err         error
	elapsed     time.Duration

	closeClk, cancelClk, openClk, okClk widget.Clickable
}

func (d *downloadDialog) start(a *App, s *capture.Session) {
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.started = time.Now()
	go func() {
		defer cancel()
		part := d.path + ".part"
		f, err := os.Create(part)
		if err == nil {
			w := bufio.NewWriterSize(f, 1<<20)
			err = a.svc.engine.Download(ctx, s, w, func(done, total int64) {
				d.done.Store(done)
				d.total.Store(total)
				now := time.Now().UnixMilli()
				if now-d.lastInv.Load() >= 100 {
					d.lastInv.Store(now)
					a.host.Invalidate()
				}
			})
			if err == nil {
				err = w.Flush()
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				os.Remove(d.path)
				err = os.Rename(part, d.path)
			}
			if err != nil {
				os.Remove(part)
			}
		}
		a.Post(func() {
			d.mu.Lock()
			d.finished, d.err, d.elapsed = true, err, time.Since(d.started)
			d.mu.Unlock()
			switch {
			case errors.Is(err, context.Canceled):
			case err != nil:
				a.Toast(toastError, "下载失败："+err.Error())
			default:
				a.Toast(toastOK, "已下载 "+filepath.Base(d.path))
			}
		})
	}()
}

func (d *downloadDialog) state() (bool, error, time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.finished, d.err, d.elapsed
}

func (d *downloadDialog) Submit(a *App) {
	if fin, _, _ := d.state(); fin {
		a.Close(d)
	}
}

func (d *downloadDialog) Cancel(a *App) {
	if d.cancel != nil {
		d.cancel()
	}
	a.Close(d)
}

func (d *downloadDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if d.cancelClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.okClk.Clicked(gtx) {
		a.Close(d)
	}
	if d.openClk.Clicked(gtx) {
		dir := filepath.Dir(d.path)
		go osutil.Open(dir)
	}
	fin, err, elapsed := d.state()
	done, total := d.done.Load(), d.total.Load()
	if !fin {
		elapsed = time.Since(d.started)
	}
	var frac float32
	// The line below the bar: left the amount, right the speed (or the
	// time taken once done).
	left, mid, right := "", "", ""
	leftColor := th.Text2
	switch {
	case fin && err != nil:
		if total > 0 {
			frac = float32(done) / float32(total)
		}
		left, leftColor = "下载失败："+err.Error(), th.Danger
	case fin:
		frac = 1
		left = "已完成 · " + fmtBytes(done)
		right = "用时 " + elapsed.Round(100*time.Millisecond).String()
	default:
		if sec := elapsed.Seconds(); sec > 0.3 && done > 0 {
			mid = fmtBytes(int64(float64(done)/sec)) + "/s"
		}
		switch {
		case total > 0:
			frac = float32(done) / float32(total)
			left = fmtBytes(done) + " / " + fmtBytes(total)
		default:
			// Unknown size: a bar sweeping back and forth.
			t := float32(elapsed.Milliseconds()%1600) / 800
			if t > 1 {
				t = 2 - t
			}
			frac = t
			left = fmtBytes(done)
			if done == 0 {
				left = "正在连接…"
			}
		}
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
	title := "正在下载"
	if fin {
		title = "下载"
	}
	return a.dialogFrame(gtx, title, 480, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return Label{Text: d.path, Size: 13, Color: th.Text, MaxLines: 3}.Layout(gtx, th)
				}),
				vspace(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.progress(gtx, frac, 6, th.Accent)
				}),
				vspace(10),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return Label{Text: left, Size: 12, Color: leftColor, MaxLines: 3}.Layout(gtx, th)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if mid == "" {
								return layout.Dimensions{}
							}
							return layout.Inset{Left: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return Label{Text: mid, Size: 12, Color: th.Text3}.Layout(gtx, th)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if right == "" {
								return layout.Dimensions{}
							}
							return layout.Inset{Left: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return Label{Text: right, Size: 12, Color: th.Text3}.Layout(gtx, th)
							})
						}),
					)
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			if !fin {
				return buttonRow(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return th.button(gtx, &d.cancelClk, "取消", nil, btnDefault)
					},
				)
			}
			if err != nil {
				return buttonRow(gtx,
					func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "关闭", nil, btnPrimary) },
				)
			}
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.openClk, "打开所在文件夹", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "关闭", nil, btnPrimary) },
			)
		})
}
