package ui

import (
	"fmt"
	"image"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/certs"
	"nlrcapture/internal/config"
	"nlrcapture/internal/proxy"
	"nlrcapture/internal/winutil"
)

// ---- Settings ---------------------------------------------------------

type settingsDialog struct {
	a                                    *App
	port, bypass, upstream, maxBody, max Field
	noDecrypt                            Field
	listenAll, decrypt, autoBypass       widget.Bool
	ignoreCert, blockQUIC                widget.Bool
	look, accent                         Segmented
	list                                 widget.List
	closeClk, okClk, noClk               widget.Clickable
	multi                                bool
}

func newSettingsDialog(a *App) *settingsDialog {
	s := a.set
	d := &settingsDialog{a: a}
	d.port = Field{Label: "端口"}
	d.port.SetText(strconv.Itoa(s.Port))
	d.bypass = Field{Label: "系统代理例外", Hint: "以分号分隔"}
	d.bypass.SetText(s.Bypass)
	d.upstream = Field{Label: "上游代理", Hint: "host:port"}
	d.upstream.SetText(s.UpstreamProxy)
	d.maxBody = Field{Label: "正文上限 MB"}
	d.maxBody.SetText(strconv.Itoa(s.MaxBodyMB))
	d.max = Field{Label: "请求数上限"}
	d.max.SetText(strconv.Itoa(s.MaxSessions))
	d.noDecrypt = Field{Label: "不解密的主机", Hint: "每行一个，支持 * 通配", Multi: true, Height: 90, Mono: true}
	d.noDecrypt.SetText(strings.Join(s.NoDecrypt, "\n"))
	d.listenAll.Value = s.ListenAll
	d.decrypt.Value = s.Decrypt
	d.autoBypass.Value = s.AutoBypass
	d.ignoreCert.Value = s.IgnoreCertErrors
	d.blockQUIC.Value = s.BlockQUIC
	d.look.Value = s.Appearance
	d.accent.Value = s.Accent
	d.list.Axis = layout.Vertical
	return d
}

func (d *settingsDialog) Multiline() bool { return d.multi }

func (d *settingsDialog) Cancel(a *App) { a.Close(d) }

func (d *settingsDialog) Submit(a *App) {
	port, err := strconv.Atoi(strings.TrimSpace(d.port.Text()))
	if err != nil || port < 1 || port > 65535 {
		a.Toast(toastError, "端口无效")
		return
	}
	maxBody, err := strconv.Atoi(strings.TrimSpace(d.maxBody.Text()))
	if err != nil || maxBody < 1 || maxBody > 1024 {
		a.Toast(toastError, "正文上限应为 1–1024")
		return
	}
	maxN, err := strconv.Atoi(strings.TrimSpace(d.max.Text()))
	if err != nil || maxN < 100 {
		a.Toast(toastError, "请求数上限至少为 100")
		return
	}
	var hosts []string
	for _, l := range strings.Split(d.noDecrypt.Text(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			hosts = append(hosts, l)
		}
	}
	old := a.set
	a.updateSettings(func(s *config.Settings) {
		s.Port, s.Bypass, s.UpstreamProxy = port, strings.TrimSpace(d.bypass.Text()), strings.TrimSpace(d.upstream.Text())
		s.MaxBodyMB, s.MaxSessions, s.NoDecrypt = maxBody, maxN, hosts
		s.ListenAll, s.Decrypt, s.AutoBypass = d.listenAll.Value, d.decrypt.Value, d.autoBypass.Value
		s.IgnoreCertErrors, s.BlockQUIC = d.ignoreCert.Value, d.blockQUIC.Value
		s.Appearance, s.Accent = d.look.Value, d.accent.Value
	})
	a.applyTheme()
	a.svc.reapply(old)
	a.Close(d)
}

func (d *settingsDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	d.multi = gtx.Focused(&d.noDecrypt.Editor)
	for _, f := range []*Field{&d.port, &d.bypass, &d.upstream, &d.maxBody, &d.max, &d.noDecrypt} {
		f.Events(gtx)
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	const lw = 104
	field := func(label string, f *Field, w unit.Dp) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return th.formRow(gtx, label, lw, 10, func(gtx layout.Context) layout.Dimensions {
				if w > 0 {
					return fixedW(gtx, w, func(gtx layout.Context) layout.Dimensions { return f.box(gtx, th, nil) })
				}
				return f.box(gtx, th, nil)
			})
		}
	}
	check := func(b *widget.Bool, label string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return th.formRow(gtx, "", lw, 0, func(gtx layout.Context) layout.Dimensions {
				return exact(gtx, gtx.Constraints.Max.X, gtx.Dp(controlH), func(gtx layout.Context) layout.Dimensions {
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, b, label) })
				})
			})
		}
	}
	// rows stacks the rows of a group.
	rows := func(ws ...layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, len(ws))
			for i, w := range ws {
				children[i] = layout.Rigid(w)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}
	}
	groups := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions {
			return th.group(gtx, "代理", nil, rows(
				field("端口", &d.port, 140),
				field("系统代理例外", &d.bypass, 0),
				field("上游代理", &d.upstream, 0),
				check(&d.listenAll, "允许局域网连接"),
			))
		},
		func(gtx layout.Context) layout.Dimensions {
			return th.group(gtx, "HTTPS", nil, rows(
				field("不解密的主机", &d.noDecrypt, 0),
				check(&d.decrypt, "解密 HTTPS"),
				check(&d.autoBypass, "证书被拒绝时自动直通"),
				check(&d.ignoreCert, "忽略服务器证书错误"),
			))
		},
		func(gtx layout.Context) layout.Dimensions {
			return th.group(gtx, "虚拟网卡", nil, rows(check(&d.blockQUIC, "阻止 QUIC")))
		},
		func(gtx layout.Context) layout.Dimensions {
			return th.group(gtx, "存储", nil, rows(
				field("正文上限 MB", &d.maxBody, 140),
				func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "请求数上限", lw, 0, func(gtx layout.Context) layout.Dimensions {
						return fixedW(gtx, 140, func(gtx layout.Context) layout.Dimensions { return d.max.box(gtx, th, nil) })
					})
				},
			))
		},
		func(gtx layout.Context) layout.Dimensions {
			return th.group(gtx, "外观", nil, rows(
				func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "主题", lw, 10, func(gtx layout.Context) layout.Dimensions {
						return d.look.Layout(gtx, th, [][2]string{{config.AppearanceSystem, "跟随系统"}, {config.AppearanceDark, "深色"}, {config.AppearanceLight, "浅色"}})
					})
				},
				func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "强调色", lw, 0, func(gtx layout.Context) layout.Dimensions {
						return d.accent.Layout(gtx, th, [][2]string{{"green", "绿"}, {"blue", "蓝"}, {"orange", "橙"}, {"purple", "紫"}})
					})
				},
			))
		},
	}
	return a.dialogFrame(gtx, "设置", 600, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return th.list(gtx, &d.list, len(groups), func(gtx layout.Context, i int) layout.Dimensions {
				top := unit.Dp(16)
				if i == 0 {
					top = 0
				}
				return layout.Inset{Top: top, Right: 4}.Layout(gtx, groups[i])
			})
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "保存", nil, btnPrimary) },
			)
		})
}

// ---- Certificate ------------------------------------------------------

type certDialog struct {
	a         *App
	firstRun  bool
	busy      bool
	decrypt   widget.Bool
	installed bool
	checked   bool

	closeClk, installClk, removeClk, exportClk, regenClk, okClk widget.Clickable
}

func newCertDialog(a *App) *certDialog {
	d := &certDialog{a: a}
	d.decrypt.Value = a.set.Decrypt
	return d
}

func (d *certDialog) Submit(a *App) { a.Close(d) }
func (d *certDialog) Cancel(a *App) { a.Close(d) }

func (d *certDialog) run(what string, f func(ca *certs.Authority) error) {
	a := d.a
	ca := a.svc.ca
	if ca == nil || d.busy {
		return
	}
	d.busy = true
	go func() {
		err := f(ca)
		a.Post(func() {
			d.busy = false
			d.checked = false
			if err != nil {
				a.Toast(toastError, what+"失败")
			} else {
				a.Toast(toastOK, "已"+what)
			}
		})
	}()
}

func (d *certDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	ca := a.svc.ca
	if !d.checked && ca != nil {
		d.checked = true
		d.installed = ca.Installed()
	}
	if d.decrypt.Update(gtx) {
		a.updateSettings(func(s *config.Settings) { s.Decrypt = d.decrypt.Value })
		a.svc.applyConfig()
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.installClk.Clicked(gtx) {
		d.run("安装", func(ca *certs.Authority) error { return ca.Install() })
	}
	if d.removeClk.Clicked(gtx) {
		d.run("卸载", func(ca *certs.Authority) error {
			_, err := ca.Uninstall()
			return err
		})
	}
	if d.exportClk.Clicked(gtx) && ca != nil {
		hwnd := a.host.HWND()
		go func() {
			p, err := winutil.SaveFile(hwnd, "导出根证书", "NLRCaptureRoot.crt", "crt", []winutil.Filter{{Name: "PEM 证书", Pattern: "*.crt;*.pem"}, {Name: "DER 证书", Pattern: "*.cer"}})
			if err != nil {
				return
			}
			err = ca.Export(p)
			a.Post(func() {
				if err != nil {
					a.Toast(toastError, "导出失败："+err.Error())
				} else {
					a.Toast(toastOK, "已导出")
				}
			})
		}()
	}
	if d.regenClk.Clicked(gtx) {
		a.Open(&confirmDialog{
			title:   "重新生成根证书",
			message: "旧证书将失效，需要重新安装。",
			okLabel: "重新生成", danger: true,
			onOK: func() {
				d.run("重新生成", func(ca *certs.Authority) error {
					ca.Uninstall()
					return ca.Regenerate()
				})
			},
		})
	}
	status, sc := "未安装", th.Warn
	if d.installed {
		status, sc = "已安装", th.Green
	}
	if ca == nil {
		status, sc = "不可用", th.Danger
	}
	fp := ""
	if ca != nil {
		fp = ca.Fingerprint()
	}
	return a.dialogFrame(gtx, "HTTPS 证书", 560, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.panel(gtx, 14, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icCert, 34, sc) }),
							hspace(12),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return column(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return th.txtW(gtx, certs.CommonName, 14, th.Text, font.SemiBold)
									}),
									vspace(4),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, status, 12, sc) }),
								)
							}),
						)
					})
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "SHA-256 指纹", nil, func(gtx layout.Context) layout.Dimensions {
						return Label{Text: fp, Size: 12, Color: th.Text, Mono: true, MaxLines: 3}.Layout(gtx, th)
					})
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.busy {
						gtx = gtx.Disabled()
					}
					return buttonRow(gtx,
						func(gtx layout.Context) layout.Dimensions {
							return th.button(gtx, &d.installClk, "安装", icCert, btnPrimary)
						},
						func(gtx layout.Context) layout.Dimensions {
							return th.button(gtx, &d.exportClk, "导出", icExport, btnDefault)
						},
						func(gtx layout.Context) layout.Dimensions {
							return th.button(gtx, &d.removeClk, "卸载", nil, btnDefault)
						},
						func(gtx layout.Context) layout.Dimensions {
							return th.button(gtx, &d.regenClk, "重新生成", nil, btnDanger)
						},
					)
				}),
				vspace(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, &d.decrypt, "解密 HTTPS") }),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return th.button(gtx, &d.okClk, "完成", nil, btnPrimary)
		})
}

// ---- AutoResponder rules ------------------------------------------------

type ruleRow struct {
	rule    proxy.Rule
	enabled widget.Bool
	clk     widget.Clickable
	del     widget.Clickable
}

// rulesDialog edits the AutoResponder rules: the list, the selected rule,
// and a box to try a URL against them.
type rulesDialog struct {
	a                       *App
	rows                    []*ruleRow
	sel                     int
	match, param, comment   Field
	test                    Field
	kind, method            Select
	addClk, closeClk, okClk widget.Clickable
	noClk                   widget.Clickable
	list                    widget.List
}

func newRulesDialog(a *App) *rulesDialog {
	d := &rulesDialog{a: a, sel: -1}
	for _, r := range a.set.Rules {
		rr := &ruleRow{rule: r}
		rr.enabled.Value = r.Enabled
		d.rows = append(d.rows, rr)
	}
	d.match = Field{Hint: "子串、* 通配、exact:网址 或 regex:表达式", Mono: true}
	d.param = Field{Mono: true}
	d.comment = Field{Hint: "可选"}
	d.test = Field{Hint: "输入网址，查看会命中哪条规则", Mono: true}
	d.list.Axis = layout.Vertical
	if len(d.rows) > 0 {
		d.pick(0)
	}
	return d
}

func (d *rulesDialog) Multiline() bool { return false }

func (d *rulesDialog) startNew(r proxy.Rule) {
	rr := &ruleRow{rule: r}
	rr.enabled.Value = r.Enabled
	d.rows = append(d.rows, rr)
	d.pick(len(d.rows) - 1)
	d.list.ScrollTo(len(d.rows) - 1)
}

// store writes the editor back into the selected rule.
func (d *rulesDialog) store() {
	if d.sel < 0 || d.sel >= len(d.rows) {
		return
	}
	r := &d.rows[d.sel].rule
	r.Match = strings.TrimSpace(d.match.Text())
	r.Action = joinAction(d.kind.Value, d.param.Text())
	r.Method = d.method.Value
	r.Comment = strings.TrimSpace(d.comment.Text())
}

func (d *rulesDialog) pick(i int) {
	d.store()
	d.sel = i
	if i < 0 || i >= len(d.rows) {
		d.sel = -1
		for _, f := range []*Field{&d.match, &d.param, &d.comment} {
			f.SetText("")
		}
		d.kind.Value, d.method.Value = "", ""
		return
	}
	r := d.rows[i].rule
	d.match.SetText(r.Match)
	kind, param := splitAction(r.Action)
	d.kind.Value = kind
	d.param.SetText(param)
	d.method.Value = strings.ToUpper(r.Method)
	d.comment.SetText(r.Comment)
}

func (d *rulesDialog) Cancel(a *App) { a.Close(d) }

func (d *rulesDialog) Submit(a *App) {
	d.store()
	var rules []proxy.Rule
	for i, rr := range d.rows {
		r := rr.rule
		r.Enabled = rr.enabled.Value
		if r.Match == "" {
			continue
		}
		if msg := actionError(splitAction(r.Action)); msg != "" {
			d.pick(i)
			a.Toast(toastError, fmt.Sprintf("第 %d 条：%s", i+1, msg))
			return
		}
		if r.ID == "" {
			r.ID = strconv.Itoa(i + 1)
		}
		rules = append(rules, r)
	}
	a.updateSettings(func(s *config.Settings) { s.Rules = rules })
	a.svc.applyConfig()
	a.Close(d)
}

func (d *rulesDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	for _, f := range []*Field{&d.match, &d.param, &d.comment, &d.test} {
		f.Events(gtx)
	}
	if d.addClk.Clicked(gtx) {
		d.startNew(proxy.Rule{Enabled: true, Action: "*404"})
		d.match.Focus(gtx)
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
		return layout.Dimensions{}
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	for i := 0; i < len(d.rows); i++ {
		rr := d.rows[i]
		if rr.del.Clicked(gtx) {
			d.store()
			d.rows = append(d.rows[:i], d.rows[i+1:]...)
			d.sel = -1
			d.pick(min(i, len(d.rows)-1))
			break
		}
		if rr.clk.Clicked(gtx) && i != d.sel {
			d.pick(i)
		}
	}
	if d.kind.Changed() {
		d.param.SetText("")
		if d.kind.Value == "status" {
			d.param.SetText("404")
		}
	}
	d.store()
	kind := actionKindOf(d.kind.Value)
	d.param.Hint = kind.hint

	// Which rule would answer the test URL.
	testMsg, testColor := "", th.Text3
	if u := strings.TrimSpace(d.test.Text()); u != "" {
		testMsg = "无匹配"
		for i, rr := range d.rows {
			if rr.enabled.Value && rr.rule.Match != "" && proxy.MatchURL(rr.rule.Match, u) {
				testMsg, testColor = fmt.Sprintf("命中第 %d 条 · %s", i+1, actionSummary(rr.rule.Action)), th.AccentText
				break
			}
		}
	}

	listGroup := func(gtx layout.Context) layout.Dimensions {
		count := func(gtx layout.Context) layout.Dimensions {
			if len(d.rows) == 0 {
				return layout.Dimensions{}
			}
			return th.txt(gtx, fmt.Sprintf("%d 条", len(d.rows)), 12, th.Text3)
		}
		return th.group(gtx, "规则", count, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(160)))
			if len(d.rows) == 0 {
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.txt(gtx, "暂无规则", 13, th.Text3)
				})
			}
			return th.list(gtx, &d.list, len(d.rows), func(gtx layout.Context, i int) layout.Dimensions {
				return d.ruleRow(gtx, i)
			})
		})
	}
	label := func(gtx layout.Context, s string) layout.Dimensions {
		c := th.Text2
		if !gtx.Enabled() {
			c = th.Text3
		}
		return th.txt(gtx, s, 13, c)
	}
	editGroup := func(gtx layout.Context) layout.Dimensions {
		return th.group(gtx, "编辑规则", nil, func(gtx layout.Context) layout.Dimensions {
			if d.sel < 0 {
				gtx = gtx.Disabled()
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "匹配", formLabelW, 10, func(gtx layout.Context) layout.Dimensions {
						return d.match.box(gtx, th, nil)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "动作", formLabelW, 10, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return fixedW(gtx, 150, func(gtx layout.Context) layout.Dimensions {
									return d.kind.Layout(gtx, a, actionOptions())
								})
							}),
							hspace(8),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								if !kind.param {
									gtx = gtx.Disabled()
								}
								return d.param.box(gtx, th, nil)
							}),
						)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.formRow(gtx, "方法", formLabelW, 0, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return fixedW(gtx, 150, func(gtx layout.Context) layout.Dimensions {
									return d.method.Layout(gtx, a, methodOptions)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return exact(gtx, gtx.Dp(60), gtx.Dp(controlH), func(gtx layout.Context) layout.Dimensions {
									return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return label(gtx, "备注") })
								})
							}),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return d.comment.box(gtx, th, nil) }),
						)
					})
				}),
			)
		})
	}
	testGroup := func(gtx layout.Context) layout.Dimensions {
		return th.group(gtx, "测试", nil, func(gtx layout.Context) layout.Dimensions {
			return d.test.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
				if testMsg == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 8, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.txt(gtx, testMsg, 12, testColor)
				})
			})
		})
	}
	return a.dialogFrame(gtx, "自动响应规则", 720, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(listGroup),
				vspace(16),
				layout.Rigid(editGroup),
				vspace(16),
				layout.Rigid(testGroup),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return footerSplit(gtx,
				[]layout.Widget{func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.addClk, "添加规则", icAdd, btnDefault)
				}},
				[]layout.Widget{
					func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
					func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "保存", nil, btnPrimary) },
				})
		})
}

func (d *rulesDialog) ruleRow(gtx layout.Context, i int) layout.Dimensions {
	th := d.a.th
	rr := d.rows[i]
	h := gtx.Dp(32)
	w := gtx.Constraints.Max.X
	return rr.clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(0, 0, w, h)
		switch {
		case i == d.sel:
			fillRR(gtx.Ops, r, gtx.Dp(6), alpha(th.Accent, 0x22))
		case rr.clk.Hovered():
			fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg3)
		}
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		layout.Inset{Left: 10, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d.a.smallCheck(gtx, &rr.enabled, "") }),
				hspace(10),
				layout.Flexed(0.55, func(gtx layout.Context) layout.Dimensions {
					m, c := rr.rule.Match, th.Text
					if m == "" {
						m, c = "未填写匹配", th.Text3
					}
					return Label{Text: m, Size: 12, Color: c, Mono: true}.Layout(gtx, th)
				}),
				hspace(12),
				layout.Flexed(0.45, func(gtx layout.Context) layout.Dimensions {
					return Label{Text: actionSummary(rr.rule.Action), Size: 12, Color: th.Text2}.Layout(gtx, th)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.iconButton(gtx, &rr.del, icDelete, 26, 15, th.Text3, false, "删除")
				}),
			)
		})
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// ---- Breakpoints ------------------------------------------------------

type breakDialog struct {
	a                      *App
	req, resp              widget.Bool
	match                  Field
	closeClk, okClk, noClk widget.Clickable
	resumeClk              widget.Clickable
}

func newBreakDialog(a *App) *breakDialog {
	d := &breakDialog{a: a}
	bp := a.set.Breakpoints
	d.req.Value, d.resp.Value = bp.Request, bp.Response
	d.match = Field{Hint: "每行一个，支持 * 通配，留空匹配全部", Mono: true, Multi: true, Height: 120}
	d.match.SetText(bp.Match)
	return d
}

func (d *breakDialog) Multiline() bool { return true }

func (d *breakDialog) Cancel(a *App) { a.Close(d) }

func (d *breakDialog) Submit(a *App) {
	a.updateSettings(func(s *config.Settings) {
		s.Breakpoints = proxy.Breakpoints{Request: d.req.Value, Response: d.resp.Value, Match: strings.Join(proxy.SplitPatterns(d.match.Text()), "\n")}
	})
	a.svc.applyConfig()
	a.Close(d)
}

func (d *breakDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	d.match.Events(gtx)
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.resumeClk.Clicked(gtx) {
		a.svc.engine.ContinueAll()
	}
	paused := len(a.svc.engine.Paused())
	return a.dialogFrame(gtx, "断点", 560, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "中断时机", nil, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, &d.req, "请求发出前") }),
							hspace(24),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, &d.resp, "响应返回前") }),
						)
					})
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "匹配网址", nil, func(gtx layout.Context) layout.Dimensions {
						return d.match.box(gtx, th, nil)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if paused == 0 {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return th.panel(gtx, 12, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return th.txt(gtx, fmt.Sprintf("已暂停 %d 个请求", paused), 13, th.Warn)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return th.button(gtx, &d.resumeClk, "全部继续", icPlay, btnDefault)
								}),
							)
						})
					})
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "保存", nil, btnPrimary) },
			)
		})
}

// ---- Composer ---------------------------------------------------------

type composeDialog struct {
	a                        *App
	method, url              Field
	headers, body            Field
	methodClk                widget.Clickable
	closeClk, sendClk, noClk widget.Clickable
}

func newComposeDialog(a *App, s *capture.Session) *composeDialog {
	d := &composeDialog{a: a}
	d.method = Field{Mono: true}
	d.url = Field{Hint: "https://example.com/api", Mono: true}
	d.headers = Field{Hint: "每行一个，名称: 值", Multi: true, Height: 150, Mono: true}
	d.body = Field{Multi: true, Height: 150, Mono: true}
	d.method.SetText("GET")
	d.headers.SetText("User-Agent: NLR Capture\nAccept: */*\n")
	if s != nil && s.Kind != capture.KindTunnel {
		d.method.SetText(s.Method)
		u := s.URL
		if strings.HasPrefix(u, "wss://") {
			u = "https://" + u[6:]
		} else if strings.HasPrefix(u, "ws://") {
			u = "http://" + u[5:]
		}
		d.url.SetText(u)
		var hs []string
		for _, f := range s.ReqHeader {
			n := strings.ToLower(f.Name)
			if strings.HasPrefix(n, ":") || n == "content-length" || n == "host" || n == "connection" {
				continue
			}
			hs = append(hs, f.Name+": "+f.Value)
		}
		d.headers.SetText(strings.Join(hs, "\n"))
		if b, err := capture.RequestBody(s); err == nil && capture.LooksText(b) {
			d.body.SetText(string(b))
			if s.ReqHeader.Get("Content-Encoding") != "" {
				var keep []string
				for _, l := range hs {
					if !strings.HasPrefix(strings.ToLower(l), "content-encoding:") {
						keep = append(keep, l)
					}
				}
				d.headers.SetText(strings.Join(keep, "\n"))
			}
		}
	}
	return d
}

func (d *composeDialog) Multiline() bool { return true }
func (d *composeDialog) Cancel(a *App)   { a.Close(d) }

func (d *composeDialog) Submit(a *App) {
	m := strings.ToUpper(strings.TrimSpace(d.method.Text()))
	u := strings.TrimSpace(d.url.Text())
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	if m == "" || u == "http://" {
		a.Toast(toastError, "请填写网址")
		return
	}
	var h capture.Header
	for _, l := range strings.Split(d.headers.Text(), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		n, v, ok := strings.Cut(l, ":")
		if !ok {
			a.Toast(toastError, "标头格式错误："+l)
			return
		}
		h = append(h, capture.Field{Name: strings.TrimSpace(n), Value: strings.TrimSpace(v)})
	}
	a.svc.engine.Send(m, u, h, []byte(d.body.Text()))
	a.Close(d)
}

func (d *composeDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	for _, f := range []*Field{&d.method, &d.url, &d.headers, &d.body} {
		f.Events(gtx)
	}
	if d.sendClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.methodClk.Clicked(gtx) {
		var items []MenuItem
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
			m := m
			items = append(items, MenuItem{Label: m, Do: func() { d.method.SetText(m) }})
		}
		a.Menu(items...)
	}
	return a.dialogFrame(gtx, "构造请求", 720, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "请求", nil, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(120), gtx.Dp(controlH)))
								return d.method.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
									return th.iconButton(gtx, &d.methodClk, icDropDown, 22, 16, th.Text3, false, "")
								})
							}),
							hspace(8),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return d.url.box(gtx, th, nil) }),
						)
					})
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "请求标头", nil, func(gtx layout.Context) layout.Dimensions { return d.headers.box(gtx, th, nil) })
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.group(gtx, "请求正文", nil, func(gtx layout.Context) layout.Dimensions { return d.body.box(gtx, th, nil) })
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.sendClk, "发送", icSend, btnPrimary)
				},
			)
		})
}
