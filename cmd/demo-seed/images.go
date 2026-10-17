package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Placeholder product photos: a flat card in the variant's colour with the
// brand, the title and the colour written on it, and "Foto contoh" at the
// foot -- plainly not a photograph, yet the variants tell apart at a glance.
// The worker makes the WebP derivatives from them as from any upload.

const side = 1200

type faces struct{ brand, title, caption, footer font.Face }

func newFaces() (faces, error) {
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return faces{}, err
	}
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return faces{}, err
	}
	var fs faces
	for _, f := range []struct {
		dst  *font.Face
		font *opentype.Font
		size float64
	}{{&fs.brand, regular, 46}, {&fs.title, bold, 80}, {&fs.caption, regular, 44}, {&fs.footer, regular, 30}} {
		if *f.dst, err = opentype.NewFace(f.font, &opentype.FaceOptions{Size: f.size, DPI: 72, Hinting: font.HintingFull}); err != nil {
			return faces{}, err
		}
	}
	return fs, nil
}

// placeholder draws one card as a PNG.
func (fs faces) placeholder(brand, title, caption string, bg color.RGBA) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)
	fg := ink(bg)
	frame := color.NRGBA{fg.R, fg.G, fg.B, 0x40} // NRGBA: translucent, not premultiplied
	for _, r := range []image.Rectangle{
		image.Rect(48, 48, side-48, 52), image.Rect(48, side-52, side-48, side-48),
		image.Rect(48, 48, 52, side-48), image.Rect(side-52, 48, side-48, side-48),
	} {
		draw.Draw(img, r, &image.Uniform{C: frame}, image.Point{}, draw.Over)
	}

	lines := wrap(fs.title, title, side-240)
	y := side/2 - len(lines)*96/2
	centre(img, fs.brand, strings.ToUpper(brand), y-40, fg)
	for i, l := range lines {
		centre(img, fs.title, l, y+60+i*96, fg)
	}
	if caption != "" {
		centre(img, fs.caption, caption, y+60+len(lines)*96+40, fg)
	}
	centre(img, fs.footer, "Foto contoh · Belli.id", side-100, color.NRGBA{fg.R, fg.G, fg.B, 0xa0})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ink is black or white, whichever reads on bg.
func ink(bg color.RGBA) color.RGBA {
	if 299*int(bg.R)+587*int(bg.G)+114*int(bg.B) > 140_000 {
		return color.RGBA{0x1a, 0x1a, 0x1a, 0xff}
	}
	return color.RGBA{0xfa, 0xfa, 0xfa, 0xff}
}

// shade darkens a colour for the detail shot, so it is not a copy of the cover.
func shade(c color.RGBA) color.RGBA {
	f := func(v uint8) uint8 { return uint8(int(v) * 82 / 100) }
	return color.RGBA{f(c.R), f(c.G), f(c.B), 0xff}
}

func centre(img draw.Image, face font.Face, s string, baseline int, c color.Color) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face}
	x := (side - d.MeasureString(s).Ceil()) / 2
	d.Dot = fixed.P(x, baseline)
	d.DrawString(s)
}

// wrap breaks s into lines no wider than width.
func wrap(face font.Face, s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		next := strings.TrimSpace(line + " " + w)
		if line != "" && font.MeasureString(face, next).Ceil() > width {
			lines = append(lines, line)
			next = w
		}
		line = next
	}
	return append(lines, line)
}
