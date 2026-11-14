// Package imgproc = pipeline gambar (M45): validasi, hapus metadata (re-encode), buat varian ukuran (srcset).
// Format keluaran JPEG (tidak ada encoder WebP/AVIF pure-Go terawat; keputusan didokumentasikan di docs/21).
package imgproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	MaxUploadBytes = 8 << 20
	MaxPixels      = 40_000_000
	MaxSide        = 10000
	JPEGQuality    = 82
)

// Widths = lebar varian yang dibuat (tidak diperbesar melebihi sumber).
var Widths = []int{400, 800, 1600}

var (
	ErrTooLarge    = errors.New("gambar terlalu besar")
	ErrBadImage    = errors.New("berkas bukan gambar JPEG/PNG/WebP yang valid")
	ErrHugeDecoded = errors.New("dimensi gambar terlalu besar")
)

type Variant struct {
	Width, Height int
	Data          []byte
}

type Result struct {
	Width, Height int
	Variants      []Variant
}

// Process membaca gambar (maks MaxUploadBytes), menolak dekompresi berlebihan, lalu membuat varian JPEG.
func Process(r io.Reader) (*Result, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxUploadBytes {
		return nil, ErrTooLarge
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBadImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrHugeDecoded
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBadImage
	}
	b := img.Bounds()
	res := &Result{Width: b.Dx(), Height: b.Dy()}
	flat := flatten(img)
	seen := map[int]bool{}
	widths := append([]int(nil), Widths...)
	for _, w := range widths {
		if w > b.Dx() {
			w = b.Dx() // sumber lebih kecil: satu varian ukuran asli
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		h := b.Dy() * w / b.Dx()
		if h < 1 {
			h = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), flat, flat.Bounds(), xdraw.Over, nil)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: JPEGQuality}); err != nil {
			return nil, fmt.Errorf("encode: %w", err)
		}
		res.Variants = append(res.Variants, Variant{Width: w, Height: h, Data: buf.Bytes()})
	}
	return res, nil
}

// flatten menimpa transparansi di atas putih (JPEG tanpa alpha).
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, b, src, b.Min, draw.Over)
	return dst
}
