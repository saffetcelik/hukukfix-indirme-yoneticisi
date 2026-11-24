// genicons renders the HukukFix icon (a white download arrow on a blue rounded
// square) at every size the app and the extension need. Pure standard library.
//
//	go run ./tools/genicons
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type pt struct{ x, y float64 }

func capsule(p, a, b pt, r float64) bool {
	abx, aby := b.x-a.x, b.y-a.y
	t := ((p.x-a.x)*abx + (p.y-a.y)*aby) / (abx*abx + aby*aby)
	t = math.Max(0, math.Min(1, t))
	dx, dy := p.x-(a.x+t*abx), p.y-(a.y+t*aby)
	return dx*dx+dy*dy <= r*r
}

func roundRect(p pt, lo, hi, r float64) bool {
	cx := math.Max(lo+r, math.Min(hi-r, p.x))
	cy := math.Max(lo+r, math.Min(hi-r, p.y))
	dx, dy := p.x-cx, p.y-cy
	return p.x >= lo && p.x <= hi && p.y >= lo && p.y <= hi && dx*dx+dy*dy <= r*r
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// render draws the icon. Small sizes use a bolder glyph and no margin so the
// arrow stays legible in the tray and the browser toolbar.
func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 6
	margin, radius, stroke := 0.03, 0.225, 0.046
	if size <= 32 {
		margin, radius, stroke = 0.0, 0.24, 0.058
	}
	top := [3]float64{0x5B, 0x8D, 0xFF}
	bot := [3]float64{0x2F, 0x5B, 0xF0}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var bg, fg float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := pt{(float64(x) + (float64(sx)+0.5)/ss) / float64(size), (float64(y) + (float64(sy)+0.5)/ss) / float64(size)}
					if !roundRect(p, margin, 1-margin, radius) {
						continue
					}
					bg++
					if capsule(p, pt{0.5, 0.25}, pt{0.5, 0.61}, stroke) ||
						capsule(p, pt{0.335, 0.455}, pt{0.5, 0.62}, stroke) ||
						capsule(p, pt{0.665, 0.455}, pt{0.5, 0.62}, stroke) ||
						capsule(p, pt{0.31, 0.765}, pt{0.69, 0.765}, stroke) {
						fg++
					}
				}
			}
			if bg == 0 {
				continue
			}
			t := float64(y) / float64(size-1)
			w := fg / bg
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(lerp(lerp(top[0], bot[0], t), 255, w)),
				G: uint8(lerp(lerp(top[1], bot[1], t), 255, w)),
				B: uint8(lerp(lerp(top[2], bot[2], t), 255, w)),
				A: uint8(255 * bg / (ss * ss)),
			})
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// dib encodes a 32-bit BMP icon image (XOR bitmap + empty AND mask).
func dib(img *image.NRGBA) []byte {
	n := img.Bounds().Dx()
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, struct {
		Size            uint32
		W, H            int32
		Planes, Bits    uint16
		Comp, ImgSize   uint32
		XPPM, YPPM      int32
		Used, Important uint32
	}{40, int32(n), int32(n * 2), 1, 32, 0, uint32(n * n * 4), 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, ((n+31)/32*4)*n))
	return b.Bytes()
}

func ico(sizes ...int) []byte {
	var imgs [][]byte
	for _, s := range sizes {
		if s >= 256 {
			imgs = append(imgs, pngBytes(render(s)))
		} else {
			imgs = append(imgs, dib(render(s)))
		}
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		b.Write([]byte{byte(s % 256), byte(s % 256), 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, d := range imgs {
		b.Write(d)
	}
	return b.Bytes()
}

func write(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	write("build/appicon.png", pngBytes(render(1024)))
	write("build/windows/icon.ico", ico(16, 24, 32, 48, 64, 256))
	write("build/tray.ico", ico(16, 20, 24, 32))
	for _, s := range []int{16, 32, 48, 128} {
		write(filepath.Join("extension", "icons", "icon"+itoa(s)+".png"), pngBytes(render(s)))
	}
	write("frontend/dist/icon.png", pngBytes(render(64)))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
