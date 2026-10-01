package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"golang.org/x/exp/shiny/materialdesign/icons"
)

func mustIcon(data []byte) *widget.Icon {
	ic, err := widget.NewIcon(data)
	if err != nil {
		panic(err)
	}
	return ic
}

var (
	icCheck     = mustIcon(icons.NavigationCheck)
	icClose     = mustIcon(icons.NavigationClose)
	icAdd       = mustIcon(icons.ContentAdd)
	icHome      = mustIcon(icons.ActionHome)
	icFolder    = mustIcon(icons.FileFolder)
	icFile      = mustIcon(icons.EditorInsertDriveFile)
	icLink      = mustIcon(icons.ContentLink)
	icRefresh   = mustIcon(icons.NavigationRefresh)
	icUp        = mustIcon(icons.NavigationArrowUpward)
	icDown      = mustIcon(icons.NavigationArrowDownward)
	icBack      = mustIcon(icons.NavigationArrowBack)
	icUpload    = mustIcon(icons.FileFileUpload)
	icDownload  = mustIcon(icons.FileFileDownload)
	icDelete    = mustIcon(icons.ActionDelete)
	icSearch    = mustIcon(icons.ActionSearch)
	icSettings  = mustIcon(icons.ActionSettings)
	icEdit      = mustIcon(icons.EditorModeEdit)
	icCopy      = mustIcon(icons.ContentContentCopy)
	icPaste     = mustIcon(icons.ContentContentPaste)
	icMore      = mustIcon(icons.NavigationMoreHoriz)
	icKey       = mustIcon(icons.CommunicationVPNKey)
	icLock      = mustIcon(icons.ActionLock)
	icServer    = mustIcon(icons.ActionDNS)
	icChevron   = mustIcon(icons.NavigationChevronRight)
	icExpand    = mustIcon(icons.NavigationExpandMore)
	icEye       = mustIcon(icons.ActionVisibility)
	icEyeOff    = mustIcon(icons.ActionVisibilityOff)
	icTransfer  = mustIcon(icons.ActionSwapVert)
	icTunnel    = mustIcon(icons.ActionSwapHoriz)
	icInfo      = mustIcon(icons.ActionInfo)
	icError     = mustIcon(icons.AlertError)
	icWarn      = mustIcon(icons.AlertWarning)
	icDone      = mustIcon(icons.ActionCheckCircle)
	icNewFolder = mustIcon(icons.FileCreateNewFolder)
	icNewFile   = mustIcon(icons.ActionNoteAdd)
	icMonitor   = mustIcon(icons.ActionTimeline)
	icSave      = mustIcon(icons.ContentSave)
	icPlay      = mustIcon(icons.AVPlayArrow)
	icStop      = mustIcon(icons.AVStop)
	icOpen      = mustIcon(icons.ActionOpenInNew)
	icFollow    = mustIcon(icons.MapsMyLocation)
	icReplay    = mustIcon(icons.AVReplay)
	icBolt      = mustIcon(icons.ImageFlashOn)
	icSend      = mustIcon(icons.ContentSend)
	icFiles     = mustIcon(icons.ActionViewList)
	icSidebar   = mustIcon(icons.ActionDashboard)
	icPower     = mustIcon(icons.ActionPowerSettingsNew)
	icHistory   = mustIcon(icons.ActionHistory)
	icTerminal  = mustIcon(icons.HardwareDesktopWindows)
	icAgent     = mustIcon(icons.HardwareSecurity)
	icJump      = mustIcon(icons.HardwareDeviceHub)
	icSelectAll = mustIcon(icons.ContentSelectAll)
	icClear     = mustIcon(icons.ContentClear)
	icCut       = mustIcon(icons.ContentContentCut)
	icBackspace = mustIcon(icons.ContentBackspace)
	icImage     = mustIcon(icons.ImageImage)
	icTasks     = mustIcon(icons.ActionAssignment)
	icUser      = mustIcon(icons.SocialPerson)
	icSweep     = mustIcon(icons.ContentDeleteSweep)
)

// drawLogo paints the "N>" mark inside a square of the given size.
func drawLogo(gtx layout.Context, size int, fg, accent color.NRGBA) layout.Dimensions {
	s := float32(size)
	p := func(x, y float32) f32.Point { return f32.Pt(x*s, y*s) }
	stroke := func(c color.NRGBA, w float32, pts ...f32.Point) {
		var path clip.Path
		path.Begin(gtx.Ops)
		path.MoveTo(pts[0])
		for _, pt := range pts[1:] {
			path.LineTo(pt)
		}
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: path.End(), Width: w * s}.Op())
	}
	// N
	stroke(fg, 0.13, p(0.14, 0.76), p(0.14, 0.24), p(0.50, 0.76), p(0.50, 0.24))
	// >
	stroke(accent, 0.11, p(0.62, 0.50), p(0.86, 0.63), p(0.62, 0.76))
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// Window control glyphs, drawn with hairlines like the native ones.
func drawWinGlyph(ops *op.Ops, kind int, center image.Point, s float32, c color.NRGBA, px float32) {
	cx, cy := float32(center.X), float32(center.Y)
	h := s / 2
	switch kind {
	case 0: // minimize
		line(ops, f32.Pt(cx-h, cy), f32.Pt(cx+h, cy), px, c)
	case 1: // maximize
		var p clip.Path
		p.Begin(ops)
		p.MoveTo(f32.Pt(cx-h, cy-h))
		p.LineTo(f32.Pt(cx+h, cy-h))
		p.LineTo(f32.Pt(cx+h, cy+h))
		p.LineTo(f32.Pt(cx-h, cy+h))
		p.Close()
		paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: px}.Op())
	case 2: // restore
		o := s * 0.22
		var p clip.Path
		p.Begin(ops)
		p.MoveTo(f32.Pt(cx-h, cy-h+o))
		p.LineTo(f32.Pt(cx+h-o, cy-h+o))
		p.LineTo(f32.Pt(cx+h-o, cy+h))
		p.LineTo(f32.Pt(cx-h, cy+h))
		p.Close()
		paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: px}.Op())
		var q clip.Path
		q.Begin(ops)
		q.MoveTo(f32.Pt(cx-h+o, cy-h+o))
		q.LineTo(f32.Pt(cx-h+o, cy-h))
		q.LineTo(f32.Pt(cx+h, cy-h))
		q.LineTo(f32.Pt(cx+h, cy+h-o))
		q.LineTo(f32.Pt(cx+h-o, cy+h-o))
		paint.FillShape(ops, c, clip.Stroke{Path: q.End(), Width: px}.Op())
	case 3: // close
		line(ops, f32.Pt(cx-h, cy-h), f32.Pt(cx+h, cy+h), px, c)
		line(ops, f32.Pt(cx-h, cy+h), f32.Pt(cx+h, cy-h), px, c)
	}
}
