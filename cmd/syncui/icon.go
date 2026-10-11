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

const iconSize = 32

var iconColors = map[string]color.NRGBA{
	kindRunning: {R: 0x2e, G: 0xa0, B: 0x43, A: 0xff},
	kindPaused:  {R: 0xe3, G: 0xa0, B: 0x08, A: 0xff},
	kindStopped: {R: 0x8a, G: 0x8f, B: 0x98, A: 0xff},
}

// iconPNG draws a filled, anti-aliased circle in the color of kind.
func iconPNG(kind string) []byte {
	c, ok := iconColors[kind]
	if !ok {
		c = iconColors[kindStopped]
	}
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	mid, r := float64(iconSize)/2, float64(iconSize)/2-2
	for y := range iconSize {
		for x := range iconSize {
			d := math.Hypot(float64(x)+0.5-mid, float64(y)+0.5-mid)
			a := math.Max(0, math.Min(1, r-d+0.5)) // one pixel of edge smoothing
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(a * 255)})
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// iconICO wraps a PNG in a one-image .ico, which is what the Windows tray
// wants (Vista and later accept PNG data inside an ICO).
func iconICO(pngData []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, struct{ Reserved, Type, Count uint16 }{0, 1, 1})
	_ = binary.Write(&b, binary.LittleEndian, struct {
		W, H, Colors, Reserved uint8
		Planes, BitCount       uint16
		Size, Offset           uint32
	}{iconSize, iconSize, 0, 0, 1, 32, uint32(len(pngData)), 6 + 16})
	b.Write(pngData)
	return b.Bytes()
}

// trayIcon is the image bytes for kind in the format this OS's tray wants.
func trayIcon(kind string) []byte {
	p := iconPNG(kind)
	if runtime.GOOS == "windows" {
		return iconICO(p)
	}
	return p
}
