package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
)

var (
	green  = color.RGBA{0x2e, 0x9e, 0x5b, 0xff} // in sync
	blue   = color.RGBA{0x2f, 0x6f, 0xdf, 0xff} // syncing
	orange = color.RGBA{0xe0, 0x8a, 0x1e, 0xff} // needs you
	grey   = color.RGBA{0x8a, 0x8a, 0x8a, 0xff} // peer offline or paused
)

// makeIcon draws a coloured disc with two white arrows (⇄) and returns it in
// the format the tray wants: ICO on Windows, PNG elsewhere.
func makeIcon(c color.RGBA) []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-15.5, float64(y)-15.5)
			if a := math.Min(1, math.Max(0, 15.5-d)); a > 0 {
				img.Set(x, y, color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(255 * a)})
			}
			ay := math.Abs(float64(y) - 11)
			by := math.Abs(float64(y) - 20)
			top := (y >= 10 && y <= 12 && x >= 7 && x <= 19) || (x >= 19 && x <= 25 && ay <= float64(25-x)*0.7)
			bottom := (y >= 19 && y <= 21 && x >= 12 && x <= 25) || (x >= 6 && x <= 12 && by <= float64(x-6)*0.7)
			if top || bottom {
				img.Set(x, y, white)
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	if runtime.GOOS != "windows" {
		return buf.Bytes()
	}
	return wrapICO(buf.Bytes(), size)
}

// wrapICO puts a PNG inside a one-image .ico container (supported since Vista).
func wrapICO(pngData []byte, size int) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(0)) // reserved
	w(uint16(1)) // type: icon
	w(uint16(1)) // image count
	w(uint8(size))
	w(uint8(size))
	w(uint8(0))  // palette size
	w(uint8(0))  // reserved
	w(uint16(1)) // colour planes
	w(uint16(32))
	w(uint32(len(pngData)))
	w(uint32(6 + 16)) // offset of the image data
	b.Write(pngData)
	return b.Bytes()
}
