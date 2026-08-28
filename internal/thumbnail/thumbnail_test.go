package thumbnail

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
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

// TestGenerate_RejectsDecompressionBomb covers the denial-of-service this
// package used to be open to. Image dimensions live in the file header
// and cost nothing to inflate, so a tiny file can claim enormous ones: a
// PNG of a single repeated colour compresses to almost nothing, meaning
// well under a megabyte of upload could make image.Decode try to
// allocate around a gigabyte at once. Thumbnails are generated eagerly at
// upload and swept again on every startup, so one such file would take
// the site down repeatedly until someone found it.
//
// Generate now reads only the header first (image.DecodeConfig) and
// refuses anything past MaxPixels before committing to the allocation.
func TestGenerate_RejectsDecompressionBomb(t *testing.T) {
	bomb := bombPNG(t, 30000, 30000)
	if len(bomb) > 2<<20 {
		t.Fatalf("the crafted bomb is %d bytes — the point is that it's small", len(bomb))
	}

	if _, err := Generate(bomb); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Generate(30000x30000 in %d bytes) = %v, want ErrTooLarge", len(bomb), err)
	}

	// An ordinary photo-sized image still works.
	ok := bombPNG(t, 800, 600)
	if _, err := Generate(ok); err != nil {
		t.Errorf("Generate on an 800x600 image failed: %v", err)
	}
}

// bombPNG hand-builds a grayscale PNG of the given dimensions whose pixel
// data is all zeros, so it compresses to a tiny file however large the
// declared dimensions are. Built by hand rather than with image/png's
// encoder, which would need the full pixel buffer in memory — the very
// allocation this test exists to avoid.
func bombPNG(t *testing.T, w, h int) []byte {
	t.Helper()

	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		c := crc32.NewIEEE()
		c.Write([]byte(typ))
		c.Write(data)
		binary.Write(&b, binary.BigEndian, c.Sum32())
		return b.Bytes()
	}

	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, uint32(w))
	binary.Write(&ihdr, binary.BigEndian, uint32(h))
	ihdr.Write([]byte{8, 0, 0, 0, 0}) // 8-bit grayscale, no interlace

	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatalf("zlib writer: %v", err)
	}
	row := make([]byte, w+1) // one filter byte + w pixel bytes, all zero
	for y := 0; y < h; y++ {
		if _, err := zw.Write(row); err != nil {
			t.Fatalf("writing row: %v", err)
		}
	}
	zw.Close()

	var png bytes.Buffer
	png.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	png.Write(chunk("IHDR", ihdr.Bytes()))
	png.Write(chunk("IDAT", z.Bytes()))
	png.Write(chunk("IEND", nil))
	return png.Bytes()
}
