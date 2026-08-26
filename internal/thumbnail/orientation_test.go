package thumbnail

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// withExifOrientation splices a minimal EXIF APP1 segment carrying the
// given Orientation tag value right after jpegBytes' SOI marker — the
// same place a real camera's JPEG writer puts it.
func withExifOrientation(t *testing.T, jpegBytes []byte, orientation uint16) []byte {
	t.Helper()
	if len(jpegBytes) < 2 || jpegBytes[0] != 0xFF || jpegBytes[1] != 0xD8 {
		t.Fatalf("not a JPEG (missing SOI)")
	}

	tiff := []byte{
		'I', 'I', // little-endian
		0x2A, 0x00, // TIFF magic (42)
		0x08, 0x00, 0x00, 0x00, // IFD0 offset = 8
		0x01, 0x00, // 1 entry
		0x12, 0x01, // tag 0x0112 (Orientation)
		0x03, 0x00, // type 3 (SHORT)
		0x01, 0x00, 0x00, 0x00, // count 1
		byte(orientation), byte(orientation >> 8), 0x00, 0x00, // value + padding
		0x00, 0x00, 0x00, 0x00, // next IFD offset (none)
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)

	segLen := len(payload) + 2
	segment := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	segment = append(segment, payload...)

	out := make([]byte, 0, len(jpegBytes)+len(segment))
	out = append(out, jpegBytes[:2]...) // SOI
	out = append(out, segment...)
	out = append(out, jpegBytes[2:]...)
	return out
}

func TestExifOrientation(t *testing.T) {
	base := encodeJPEG(t, 10, 10)

	t.Run("no EXIF returns 1", func(t *testing.T) {
		if got := exifOrientation(base); got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
	t.Run("not a JPEG returns 1", func(t *testing.T) {
		if got := exifOrientation([]byte("not a jpeg")); got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
	for orientation := 1; orientation <= 8; orientation++ {
		t.Run(fmt.Sprintf("reads embedded orientation %d", orientation), func(t *testing.T) {
			withTag := withExifOrientation(t, base, uint16(orientation))
			if got := exifOrientation(withTag); got != orientation {
				t.Errorf("orientation %d: got %d", orientation, got)
			}
		})
	}
}

// markedImage builds a w x h RGBA image with a single distinct marker
// pixel at (markX, markY) — everything else black — so a transform's
// correctness can be checked by finding where the marker ends up
// instead of comparing every pixel.
func markedImage(w, h, markX, markY int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	img.Set(markX, markY, color.RGBA{255, 255, 255, 255})
	return img
}

func findMarker(t *testing.T, img image.Image) (int, int) {
	t.Helper()
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 > 200 && bl>>8 > 200 {
				return x - b.Min.X, y - b.Min.Y
			}
		}
	}
	t.Fatalf("marker pixel not found")
	return -1, -1
}

func TestApplyOrientation(t *testing.T) {
	// A 4-wide x 2-tall image with its marker at the top-left corner —
	// physically rotating/flipping this by hand for each case gives an
	// unambiguous expected marker position, independent of the formula
	// under test.
	const w, h = 4, 2

	cases := []struct {
		orientation int
		wantW       int
		wantH       int
		wantX       int
		wantY       int
	}{
		{1, w, h, 0, 0},         // unchanged
		{2, w, h, w - 1, 0},     // mirrored horizontally: top-left -> top-right
		{3, w, h, w - 1, h - 1}, // rotated 180: top-left -> bottom-right
		{4, w, h, 0, h - 1},     // mirrored vertically: top-left -> bottom-left
		{5, h, w, 0, 0},         // transposed: top-left is a fixed point
		{6, h, w, h - 1, 0},     // rotated 90 CW: top-left -> top-right
		{7, h, w, h - 1, w - 1}, // transverse: top-left -> bottom-right
		{8, h, w, 0, w - 1},     // rotated 90 CCW: top-left -> bottom-left
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("orientation %d", c.orientation), func(t *testing.T) {
			src := markedImage(w, h, 0, 0)
			out := applyOrientation(src, c.orientation)
			b := out.Bounds()
			if b.Dx() != c.wantW || b.Dy() != c.wantH {
				t.Fatalf("orientation %d: size = %dx%d, want %dx%d", c.orientation, b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
			gotX, gotY := findMarker(t, out)
			if gotX != c.wantX || gotY != c.wantY {
				t.Errorf("orientation %d: marker at (%d,%d), want (%d,%d)", c.orientation, gotX, gotY, c.wantX, c.wantY)
			}
		})
	}
}

func TestGenerateAppliesEmbeddedOrientation(t *testing.T) {
	// A tall (portrait, after correction) photo stored sideways the way
	// a phone camera would, with orientation 6 (rotate 90 CW to fix) —
	// Generate should hand back an image whose longest side (the
	// corrected height) is the one capped at MaxDimension, not the
	// stored width.
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1500)) // stored landscape
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	withTag := withExifOrientation(t, buf.Bytes(), 6)

	out, err := Generate(withTag)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	w, h := decodedSize(t, out)
	if h != MaxDimension {
		t.Errorf("height = %d, want %d (corrected orientation should be portrait)", h, MaxDimension)
	}
	if w != MaxDimension/2 {
		t.Errorf("width = %d, want %d", w, MaxDimension/2)
	}
}
