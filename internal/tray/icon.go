package tray

import (
	"bytes"
	"encoding/binary"
	"image/color"
)

// Icon colours by state.
var (
	colorRunning = color.NRGBA{0x2E, 0x9E, 0x5B, 0xFF} // green: all lines UP
	colorWarning = color.NRGBA{0xE0, 0xA1, 0x00, 0xFF} // amber: running, a line is DOWN
	colorStopped = color.NRGBA{0x80, 0x80, 0x80, 0xFF} // grey: paused or not running
)

// makeIcon draws the tray icon (a rounded square with two white bars, the
// two lines) in the given colour, at 16 and 32 px, as .ico bytes.
func makeIcon(c color.NRGBA) []byte {
	return encodeICO(drawIcon(16, c), drawIcon(32, c))
}

type img struct {
	size int
	px   []color.NRGBA // row-major, top row first
}

func drawIcon(n int, c color.NRGBA) img {
	m := img{size: n, px: make([]color.NRGBA, n*n)}
	r := n / 5 // corner radius
	inRounded := func(x, y int) bool {
		cx, cy := x, y
		switch {
		case x < r:
			cx = r
		case x >= n-r:
			cx = n - r - 1
		}
		switch {
		case y < r:
			cy = r
		case y >= n-r:
			cy = n - r - 1
		}
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= r*r
	}
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	bar := n / 8
	if bar < 2 {
		bar = 2
	}
	y1, y2 := n*5/16, n*11/16-bar // two horizontal bars
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !inRounded(x, y) {
				continue // transparent
			}
			p := c
			inBar := x >= n/5 && x < n-n/5 && ((y >= y1 && y < y1+bar) || (y >= y2 && y < y2+bar))
			if inBar {
				p = white
			}
			m.px[y*n+x] = p
		}
	}
	return m
}

// encodeICO writes images as an .ico file with 32-bit BGRA DIBs: ICONDIR,
// one ICONDIRENTRY per image, then for each image a BITMAPINFOHEADER (height
// doubled, as icons require), the pixels bottom-up, and an all-zero AND mask
// (transparency comes from the alpha channel).
func encodeICO(images ...img) []byte {
	var out bytes.Buffer
	le := func(v any) { binary.Write(&out, binary.LittleEndian, v) }

	le(uint16(0))           // reserved
	le(uint16(1))           // type: icon
	le(uint16(len(images))) // count

	offset := 6 + 16*len(images)
	var data [][]byte
	for _, m := range images {
		d := dib(m)
		data = append(data, d)
		dim := uint8(m.size)
		if m.size >= 256 {
			dim = 0
		}
		le(dim)            // width
		le(dim)            // height
		le(uint8(0))       // palette colours
		le(uint8(0))       // reserved
		le(uint16(1))      // planes
		le(uint16(32))     // bits per pixel
		le(uint32(len(d))) // bytes in image
		le(uint32(offset)) // offset of image
		offset += len(d)
	}
	for _, d := range data {
		out.Write(d)
	}
	return out.Bytes()
}

func dib(m img) []byte {
	n := m.size
	maskRow := (n + 31) / 32 * 4
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))                // biSize
	le(int32(n))                  // biWidth
	le(int32(2 * n))              // biHeight: colour + mask
	le(uint16(1))                 // biPlanes
	le(uint16(32))                // biBitCount
	le(uint32(0))                 // biCompression: BI_RGB
	le(uint32(n*n*4 + maskRow*n)) // biSizeImage
	le(int32(0))                  // biXPelsPerMeter
	le(int32(0))                  // biYPelsPerMeter
	le(uint32(0))                 // biClrUsed
	le(uint32(0))                 // biClrImportant
	for y := n - 1; y >= 0; y-- { // bottom-up
		for x := 0; x < n; x++ {
			p := m.px[y*n+x]
			b.Write([]byte{p.B, p.G, p.R, p.A})
		}
	}
	b.Write(make([]byte, maskRow*n))
	return b.Bytes()
}
