package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

func decodedSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decoding generated thumbnail: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestGenerateScalesDownLandscape(t *testing.T) {
	src := encodeJPEG(t, 3000, 1500)
	out, err := Generate(src)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	w, h := decodedSize(t, out)
	if w != MaxDimension {
		t.Errorf("width = %d, want %d", w, MaxDimension)
	}
	if h != MaxDimension/2 {
		t.Errorf("height = %d, want %d (aspect ratio preserved)", h, MaxDimension/2)
	}
}

func TestGenerateScalesDownPortrait(t *testing.T) {
	src := encodeJPEG(t, 1200, 2400)
	out, err := Generate(src)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	w, h := decodedSize(t, out)
	if h != MaxDimension {
		t.Errorf("height = %d, want %d", h, MaxDimension)
	}
	if w != MaxDimension/2 {
		t.Errorf("width = %d, want %d (aspect ratio preserved)", w, MaxDimension/2)
	}
}

func TestGenerateReencodesSmallImageWithoutUpscaling(t *testing.T) {
	src := encodePNG(t, 100, 50)
	out, err := Generate(src)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	w, h := decodedSize(t, out)
	if w != 100 || h != 50 {
		t.Errorf("size = %dx%d, want unchanged 100x50 (no upscaling)", w, h)
	}
	// Always re-encoded as JPEG, even though the source was a PNG.
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Errorf("format = %q, err = %v; want jpeg", format, err)
	}
}

func TestGenerateFlattensTransparencyToWhite(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	// Left half fully transparent, right half opaque red.
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			if x < 10 {
				img.Set(x, y, color.RGBA{0, 0, 0, 0})
			} else {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}

	out, err := Generate(buf.Bytes())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding generated thumbnail: %v", err)
	}
	r, g, b, _ := decoded.At(1, 10).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("transparent region = rgb(%d,%d,%d), want near-white, not black", r>>8, g>>8, b>>8)
	}
}

func TestGenerateRejectsNonImageBytes(t *testing.T) {
	_, err := Generate([]byte("%PDF-1.4 not actually an image"))
	if err != ErrNotAnImage {
		t.Errorf("err = %v, want ErrNotAnImage", err)
	}
}
