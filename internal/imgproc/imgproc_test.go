package imgproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makePNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 120, A: 200})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestProcessMakesJPEGVariantsWithoutUpscaling(t *testing.T) {
	res, err := Process(bytes.NewReader(makePNG(1000, 500)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Width != 1000 || res.Height != 500 {
		t.Fatalf("dimensi %dx%d", res.Width, res.Height)
	}
	// 400, 800, lalu 1600 → dibatasi 1000
	got := []int{}
	for _, v := range res.Variants {
		got = append(got, v.Width)
		if _, err := jpeg.Decode(bytes.NewReader(v.Data)); err != nil {
			t.Fatalf("varian %d bukan JPEG valid: %v", v.Width, err)
		}
		if v.Height != v.Width/2 {
			t.Fatalf("rasio rusak: %dx%d", v.Width, v.Height)
		}
	}
	want := []int{400, 800, 1000}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("lebar varian %v, mau %v", got, want)
		}
	}
}

func TestProcessSmallImageSingleVariant(t *testing.T) {
	res, err := Process(bytes.NewReader(makePNG(100, 100)))
	if err != nil || len(res.Variants) != 1 || res.Variants[0].Width != 100 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestProcessRejectsGarbageAndOversize(t *testing.T) {
	if _, err := Process(bytes.NewReader([]byte("bukan gambar"))); err != ErrBadImage {
		t.Fatalf("err=%v", err)
	}
	if _, err := Process(bytes.NewReader(bytes.Repeat([]byte{0}, MaxUploadBytes+10))); err != ErrTooLarge {
		t.Fatalf("err=%v", err)
	}
	if _, err := Process(bytes.NewReader(makePNG(10001, 1))); err != ErrHugeDecoded {
		t.Fatalf("sisi > MaxSide harus ditolak: %v", err)
	}
}
