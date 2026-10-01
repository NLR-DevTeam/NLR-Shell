package ui

import (
	"fmt"
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
)

// tasks draws the transfer list shown beside the file list.
func (fv *filesView) tasks(gtx layout.Context) layout.Dimensions {
	a := fv.a
	th := a.th
	size := gtx.Constraints.Max
	fill(gtx.Ops, image.Rect(0, 0, max(gtx.Dp(1), 1), size.Y), th.Border)
	list := fv.sv.sess.Transfers.List()

	seen := map[int]bool{}
	for _, ti := range list {
		seen[ti.ID] = true
		clk := fv.tBtns[ti.ID]
		if clk == nil {
			clk = new(widget.Clickable)
			fv.tBtns[ti.ID] = clk
		}
		if clk.Clicked(gtx) {
			switch ti.State {
			case sshx.TransferQueued, sshx.TransferRunning:
				fv.sv.sess.Transfers.Cancel(ti.ID)
			case sshx.TransferDone:
				if !ti.Upload && !ti.Temp {
					revealInExplorer(ti.Local)
				}
			}
		}
	}
	for id := range fv.tBtns {
		if !seen[id] {
			delete(fv.tBtns, id)
		}
	}

	hdrH := gtx.Dp(26)
	gtx.Constraints = layout.Exact(size)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(size.X, hdrH))
			layout.Inset{Left: 14, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, "任务", 11, th.Text3, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						finished := false
						for _, ti := range list {
							if ti.State != sshx.TransferQueued && ti.State != sshx.TransferRunning {
								finished = true
							}
						}
						if !finished {
							gtx = gtx.Disabled()
						}
						return th.iconButton(gtx, &fv.clearBtn, icSweep, 22, 15, th.Text2, false, "清除已完成")
					}),
					hspace(2),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, &fv.tasksClose, icClose, 22, 14, th.Text2, false, "关闭")
					}),
				)
			})
			fill(gtx.Ops, image.Rect(0, hdrH-max(gtx.Dp(1), 1), size.X, hdrH), th.Border)
			return layout.Dimensions{Size: image.Pt(size.X, hdrH)}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if len(list) == 0 {
				fv.message(gtx, gtx.Constraints.Max.Y, icTransfer, th.Text3, "无任务")
				return layout.Dimensions{Size: gtx.Constraints.Max}
			}
			return th.list(gtx, &fv.tlist, len(list), func(gtx layout.Context, i int) layout.Dimensions {
				return fv.taskRow(gtx, list[i])
			})
		}),
	)
	return layout.Dimensions{Size: size}
}

func (fv *filesView) taskRow(gtx layout.Context, ti sshx.TransferInfo) layout.Dimensions {
	th := fv.a.th
	frac := float32(0)
	if ti.Total > 0 {
		frac = float32(ti.Done) / float32(ti.Total)
	}
	status, sc, barC := "", th.Text3, th.Accent
	switch ti.State {
	case sshx.TransferQueued:
		status = "排队中"
	case sshx.TransferRunning:
		status = fmt.Sprintf("%s / %s · %s", fmtBytes(uint64(ti.Done)), fmtBytes(uint64(ti.Total)), fmtRate(ti.Rate))
	case sshx.TransferDone:
		status, frac = fmtBytes(uint64(ti.Total)), 1
	case sshx.TransferFailed:
		status, sc, barC = ti.Err, th.Danger, th.Danger
	case sshx.TransferCanceled:
		status, barC = "已取消", th.Text3
	}
	ic, c := icDownload, th.Accent
	if ti.Upload {
		ic, c = icUpload, th.Blue
	}
	switch ti.State {
	case sshx.TransferFailed:
		c = th.Danger
	case sshx.TransferCanceled:
		c = th.Text3
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Left: 14, Right: 6, Top: 7, Bottom: 7}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 16, c) }),
			hspace(10),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return column(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, ti.Name, 13, th.Text) }),
					vspace(5),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.progress(gtx, frac, 3, barC) }),
					vspace(4),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, status, 11, sc) }),
				)
			}),
			hspace(4),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				clk := fv.tBtns[ti.ID]
				switch ti.State {
				case sshx.TransferQueued, sshx.TransferRunning:
					return th.iconButton(gtx, clk, icClose, 26, 15, th.Text2, false, "取消")
				case sshx.TransferDone:
					if !ti.Upload && !ti.Temp {
						return th.iconButton(gtx, clk, icOpen, 26, 15, th.Text2, false, "打开位置")
					}
				}
				return layout.Dimensions{Size: image.Pt(gtx.Dp(26), gtx.Dp(26))}
			}),
		)
	})
}
