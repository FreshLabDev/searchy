// Package collage renders a set of media thumbnails into a single, tidy grid
// image (a "contact sheet"): each cell is center-crop "cover" scaled, carries a
// small numbered badge in the top-left corner, and — for videos — a small play
// badge in the top-right. The bot sends this one JPEG instead of a flood of
// individual photos, with inline buttons to page or pull a single item full.
package collage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // register GIF decoder
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"strconv"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// Cell is one tile of the grid. Data is the raw (encoded) thumbnail bytes; an
// empty/undecodable Data still produces a numbered placeholder tile so numbering
// stays aligned with the pick buttons.
type Cell struct {
	Data    []byte
	IsVideo bool
	Number  int
}

const (
	cellSize = 256 // px, square tile
	gap      = 6   // px between tiles
	pad      = 12  // px outer margin
	cols     = 4   // tiles per row: a full page of 10 → 4/4/2, short rows centered.
	// 4-wide keeps the canvas ≤ ~1066px (Telegram's ≤1280 long-side hint) and the
	// tiles/badges readable on a phone; 5-wide produced ~77px tiles.

	// maxDecodePixels caps an untrusted source's DECLARED width×height before we
	// allocate its full pixel buffer. Covers come from arbitrary search-result
	// hosts; the 5 MiB download cap bounds only the *compressed* size, and a tiny
	// PNG/WebP/GIF can declare e.g. 30000×30000 and decode to gigabytes (a
	// decompression bomb). 16 MP is already far past what a 256px tile needs.
	maxDecodePixels = 16 * 1024 * 1024
)

var (
	bgColor   = color.RGBA{0x15, 0x16, 0x18, 0xff} // canvas
	tileColor = color.RGBA{0x2b, 0x2c, 0x30, 0xff} // placeholder tile
	badgeFace font.Face
)

func init() {
	if ft, err := opentype.Parse(gobold.TTF); err == nil {
		badgeFace, _ = opentype.NewFace(ft, &opentype.FaceOptions{Size: 26, DPI: 72, Hinting: font.HintingFull})
	}
}

// Render lays out the cells into a single JPEG. cols tiles per row; the last row
// may be short. Returns an error only if there are no cells or encoding fails.
func Render(cells []Cell) ([]byte, error) {
	n := len(cells)
	if n == 0 {
		return nil, errors.New("collage: no cells")
	}
	c := cols
	if n < cols {
		c = n
	}
	rows := (n + c - 1) / c

	w := pad*2 + c*cellSize + (c-1)*gap
	h := pad*2 + rows*cellSize + (rows-1)*gap
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(bgColor), image.Point{}, draw.Src)

	fullWidth := c*cellSize + (c-1)*gap
	for i, cell := range cells {
		col, row := i%c, i/c
		// Centre a short last row instead of left-aligning it (avoids ragged dead
		// space when n isn't a multiple of c).
		rowCount := c
		if rem := n - row*c; rem < c {
			rowCount = rem
		}
		xOffset := (fullWidth - (rowCount*cellSize + (rowCount-1)*gap)) / 2
		x0 := pad + xOffset + col*(cellSize+gap)
		y0 := pad + row*(cellSize+gap)
		rect := image.Rect(x0, y0, x0+cellSize, y0+cellSize)

		draw.Draw(canvas, rect, image.NewUniform(tileColor), image.Point{}, draw.Src)
		if len(cell.Data) > 0 && safeToDecode(cell.Data) {
			if img, _, err := image.Decode(bytes.NewReader(cell.Data)); err == nil && img != nil {
				drawCover(canvas, rect, img)
			}
		}
		if cell.IsVideo {
			drawPlayBadge(canvas, rect)
		}
		drawNumberBadge(canvas, x0, y0, cell.Number)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 87}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// safeToDecode reads only the image header (no pixel buffer is allocated) and
// rejects sources whose declared dimensions would balloon into a multi-GB
// allocation. Oversized/undecodable data falls through to the placeholder tile,
// keeping numbering aligned with the pick buttons.
func safeToDecode(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return false
	}
	return int64(cfg.Width)*int64(cfg.Height) <= maxDecodePixels
}

// drawCover scales src to fully cover rect (center-cropping the overflow axis).
func drawCover(dst *image.RGBA, rect image.Rectangle, src image.Image) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	cellAspect := float64(rect.Dx()) / float64(rect.Dy())
	srcAspect := float64(sw) / float64(sh)
	var sr image.Rectangle
	if srcAspect > cellAspect { // source too wide → crop sides
		nw := int(float64(sh) * cellAspect)
		x0 := b.Min.X + (sw-nw)/2
		sr = image.Rect(x0, b.Min.Y, x0+nw, b.Max.Y)
	} else { // source too tall → crop top/bottom
		nh := int(float64(sw) / cellAspect)
		y0 := b.Min.Y + (sh-nh)/2
		sr = image.Rect(b.Min.X, y0, b.Max.X, y0+nh)
	}
	// ApproxBiLinear is far cheaper than CatmullRom and visually indistinguishable
	// when downscaling photos to a 256px tile.
	xdraw.ApproxBiLinear.Scale(dst, rect, src, sr, xdraw.Over, nil)
}

// drawNumberBadge paints a small translucent box with the cell's number, white
// and bold, in the top-left corner.
func drawNumberBadge(dst *image.RGBA, x0, y0, num int) {
	label := strconv.Itoa(num)
	bw := 30
	if len(label) > 1 {
		bw = 44
	}
	const bh = 30
	bx0, by0 := x0+6, y0+6
	fillRect(dst, image.Rect(bx0, by0, bx0+bw, by0+bh), color.RGBA{0, 0, 0, 0xb4})
	if badgeFace != nil {
		d := &font.Drawer{Dst: dst, Src: image.NewUniform(color.White), Face: badgeFace}
		adv := d.MeasureString(label).Round()
		d.Dot = fixed.P(bx0+(bw-adv)/2, by0+22)
		d.DrawString(label)
	}
}

// drawPlayBadge paints a small translucent box with a white "play" triangle in
// the top-right corner to mark video tiles.
func drawPlayBadge(dst *image.RGBA, rect image.Rectangle) {
	const size = 34
	bx1, by0 := rect.Max.X-6, rect.Min.Y+6
	bx0, by1 := bx1-size, by0+size
	fillRect(dst, image.Rect(bx0, by0, bx1, by1), color.RGBA{0, 0, 0, 0xb4})
	// Right-pointing triangle centered in the badge.
	const tw, th = 13, 16
	left := bx0 + (size-tw)/2 + 1
	top := by0 + (size-th)/2
	fillPlay(dst, left, top, tw, th, color.White)
}

// fillRect fills r with c using source-over (so translucent c blends).
func fillRect(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Over)
}

// fillPlay draws a solid right-pointing triangle: a vertical left edge of height
// `height` at x=left, tapering to an apex at (left+width, top+height/2).
func fillPlay(dst *image.RGBA, left, top, width, height int, c color.Color) {
	if width <= 0 || height <= 0 {
		return
	}
	ymid := top + height/2
	for x := 0; x <= width; x++ {
		half := (height / 2) * (width - x) / width
		for y := ymid - half; y <= ymid+half; y++ {
			dst.Set(left+x, y, c)
		}
	}
}
