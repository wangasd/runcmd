//go:build ignore

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// sky-blue palette
var (
	skyMain  = color.NRGBA{41, 171, 226, 255}  // #29ABE2
	skyLight = color.NRGBA{80, 195, 240, 255}  // lighter top
	white    = color.NRGBA{255, 255, 255, 255}
	transp   = color.NRGBA{0, 0, 0, 0}
)

// alpha-blend src over dst
func blend(dst, src color.NRGBA, a float64) color.NRGBA {
	a = math.Max(0, math.Min(1, a))
	inv := 1 - a
	return color.NRGBA{
		R: uint8(float64(src.R)*a + float64(dst.R)*inv),
		G: uint8(float64(src.G)*a + float64(dst.G)*inv),
		B: uint8(float64(src.B)*a + float64(dst.B)*inv),
		A: uint8(math.Min(255, float64(dst.A)*inv+255*a)),
	}
}

// set pixel with alpha blending
func setAA(img *image.NRGBA, x, y int, c color.NRGBA, alpha float64) {
	if x < 0 || y < 0 || x >= img.Bounds().Max.X || y >= img.Bounds().Max.Y {
		return
	}
	orig := img.NRGBAAt(x, y)
	img.SetNRGBA(x, y, blend(orig, c, alpha))
}

// draw anti-aliased line (Xiaolin Wu)
func drawLine(img *image.NRGBA, x0, y0, x1, y1 float64, c color.NRGBA, width float64) {
	dx := x1 - x0
	dy := y1 - y0
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 {
		return
	}
	steps := int(length*2) + 1
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px := x0 + t*dx
		py := y0 + t*dy
		// draw perpendicular width
		nx := -dy / length
		ny := dx / length
		half := width / 2
		for w := -half - 1; w <= half+1; w += 0.5 {
			qx := px + nx*w
			qy := py + ny*w
			dist := math.Abs(w)
			var alpha float64
			if dist < half-0.5 {
				alpha = 1.0
			} else {
				alpha = math.Max(0, half+0.5-dist)
			}
			if alpha > 0 {
				setAA(img, int(math.Round(qx)), int(math.Round(qy)), c, alpha)
			}
		}
	}
}

// rounded rect with per-pixel distance
func drawRoundedRect(img *image.NRGBA, x0, y0, x1, y1, radius float64, fill color.NRGBA) {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			// find nearest corner center
			cx := math.Max(x0+radius, math.Min(x1-radius, fx))
			cy := math.Max(y0+radius, math.Min(y1-radius, fy))
			dist := math.Sqrt((fx-cx)*(fx-cx)+(fy-cy)*(fy-cy)) - radius
			var alpha float64
			if dist <= -0.5 {
				alpha = 1
			} else if dist < 0.5 {
				alpha = 0.5 - dist
			}
			if alpha > 0 {
				orig := img.NRGBAAt(x, y)
				img.SetNRGBA(x, y, blend(orig, fill, alpha))
			}
		}
	}
}

func makeIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	r := s / 5.0

	// ── background ──────────────────────────────────────────────
	drawRoundedRect(img, 0, 0, s, s, r, skyMain)
	// top gradient highlight: top 35% blended with skyLight
	for y := 0; y < size; y++ {
		fy := float64(y) / s
		if fy > 0.38 {
			break
		}
		a := (0.38 - fy) / 0.38 * 0.45
		for x := 0; x < size; x++ {
			orig := img.NRGBAAt(x, y)
			if orig.A == 0 {
				continue
			}
			img.SetNRGBA(x, y, blend(orig, skyLight, a))
		}
	}

	// ── ">" chevron ─────────────────────────────────────────────
	lw := s * 0.080
	cx := s * 0.40
	cy := s * 0.500
	h  := s * 0.228
	dx := s * 0.168

	drawLine(img, cx-dx, cy-h, cx+dx, cy,      white, lw)
	drawLine(img, cx+dx, cy,   cx-dx, cy+h,    white, lw)

	// ── "_" cursor ───────────────────────────────────────────────
	curY  := cy + h*0.50
	curX1 := cx + dx*1.20
	curX2 := cx + dx*2.12
	curW  := lw * 0.80
	drawLine(img, curX1, curY, curX2, curY, white, curW)

	return img
}

func save(img *image.NRGBA, path string) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func main() {
	// SPK 顶层图标
	save(makeIcon(72),  "PACKAGE_ICON.PNG")
	save(makeIcon(256), "PACKAGE_ICON_256.PNG")
	// ui/images/ 多尺寸（DSM 全部应用用）
	if err := os.MkdirAll("ui/images", 0755); err != nil {
		panic(err)
	}
	for _, sz := range []int{16, 24, 32, 48, 64, 72, 96, 128, 256} {
		save(makeIcon(sz), fmt.Sprintf("ui/images/icon_%d.png", sz))
	}
	println("all icons generated")
}
