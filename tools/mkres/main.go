// Command mkres draws the NLR Shell icon and writes the Windows resource
// object (icon, manifest, version info) that the Go linker embeds into the
// executable. Run it from the repository root:
//
//	go run ./tools/mkres
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

type pt struct{ x, y float64 }

// segDist returns the distance from p to the segment ab.
func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

// polyDist returns the distance from p to a polyline.
func polyDist(p pt, pts []pt) float64 {
	d := math.MaxFloat64
	for i := 0; i+1 < len(pts); i++ {
		d = math.Min(d, segDist(p, pts[i], pts[i+1]))
	}
	return d
}

// cover converts a signed distance (negative inside) to pixel coverage.
func cover(d, px float64) float64 {
	return math.Max(0, math.Min(1, 0.5-d/px))
}

// render draws the icon at the given size. Coordinates are in a unit square.
func render(size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	px := 1 / float64(size)
	n := []pt{{0.20, 0.72}, {0.20, 0.28}, {0.52, 0.72}, {0.52, 0.28}}
	chev := []pt{{0.62, 0.50}, {0.82, 0.61}, {0.62, 0.72}}
	const (
		radius = 0.22
		nW     = 0.055
		cW     = 0.048
	)
	bgTop, bgBot := [3]float64{0x1b, 0x21, 0x29}, [3]float64{0x0b, 0x0d, 0x10}
	fg := [3]float64{0xe6, 0xea, 0xf0}
	accent := [3]float64{0x00, 0xb9, 0x35}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			p := pt{(float64(x) + 0.5) * px, (float64(y) + 0.5) * px}
			// Rounded square: distance to a box shrunk by the radius.
			qx := math.Abs(p.x-0.5) - (0.5 - radius - 0.02)
			qy := math.Abs(p.y-0.5) - (0.5 - radius - 0.02)
			box := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - radius
			a := cover(box, px)
			if a == 0 {
				continue
			}
			var c [3]float64
			for i := range c {
				c[i] = bgTop[i] + (bgBot[i]-bgTop[i])*p.y
			}
			// A hairline border lifts the tile off dark taskbars.
			edge := cover(math.Abs(box+0.012)-0.006, px)
			for i := range c {
				c[i] += (0x3a - c[i]) * edge * 0.9
			}
			blend := func(col [3]float64, cov float64) {
				for i := range c {
					c[i] += (col[i] - c[i]) * cov
				}
			}
			blend(fg, cover(polyDist(p, n)-nW, px))
			blend(accent, cover(polyDist(p, chev)-cW, px))
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(c[0] + 0.5), G: uint8(c[1] + 0.5), B: uint8(c[2] + 0.5), A: uint8(a*255 + 0.5)})
		}
	}
	return img
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	var images []image.Image
	for _, s := range []int{256, 128, 64, 48, 40, 32, 24, 20, 16} {
		images = append(images, render(s))
	}
	must(os.MkdirAll("build", 0o755))
	f, err := os.Create("build/icon.png")
	must(err)
	must(png.Encode(f, images[0]))
	must(f.Close())

	icon, err := winres.NewIconFromImages(images)
	must(err)
	ico, err := os.Create("build/icon.ico")
	must(err)
	must(icon.SaveICO(ico))
	must(ico.Close())

	rs := winres.ResourceSet{}
	// Gio loads the window icon from resource id 1.
	must(rs.SetIcon(winres.ID(1), icon))
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "NLR.Shell", Version: [4]uint16{1, 2, 1, 0}},
		Description:         "NLR Shell",
		DPIAwareness:        winres.DPIPerMonitorV2,
		LongPathAware:       true,
		UseCommonControlsV6: true,
	})
	var vi version.Info
	vi.SetFileVersion("1.2.1.0")
	vi.SetProductVersion("1.2.1.0")
	for k, v := range map[string]string{
		version.ProductName:      "NLR Shell",
		version.FileDescription:  "NLR Shell",
		version.OriginalFilename: "NLRShell.exe",
		version.InternalName:     "NLRShell",
		version.ProductVersion:   "1.2.1",
		version.FileVersion:      "1.2.1",
	} {
		must(vi.Set(version.LangDefault, k, v))
	}
	rs.SetVersionInfo(vi)

	out, err := os.Create("rsrc_windows_amd64.syso")
	must(err)
	must(rs.WriteObject(out, winres.ArchAMD64))
	must(out.Close())
	fmt.Println("wrote build/icon.png, build/icon.ico, rsrc_windows_amd64.syso")
}
