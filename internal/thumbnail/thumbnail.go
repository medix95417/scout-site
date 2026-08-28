// Package thumbnail resizes an uploaded photo down to a small preview
// size. Every gallery, carousel, and picker on the site shows many
// photos at once at a few hundred pixels wide at most; without this,
// each one downloads the visitor's full original camera photo (often
// several megabytes) just to shrink it in CSS, which is what made pages
// with a lot of photos slow to load — especially over a slow connection,
// where the homepage's auto-advancing carousel could move on to the next
// photo before the current one even finished downloading.
package thumbnail

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
)

// MaxDimension is the longest side, in pixels, a generated thumbnail is
// scaled down to. Comfortably larger than any on-screen size the site
// actually displays a thumbnail at, even at 2x pixel density, so it
// still looks sharp while being a fraction of a typical camera photo's
// size.
const MaxDimension = 640

// Quality is the JPEG encoding quality used for generated thumbnails —
// chosen for a good size/quality tradeoff for photos that are only ever
// shown small.
const Quality = 78

// MaxPixels caps how large a source image may be, in total pixels,
// before this package will decode it at all — 50 megapixels, comfortably
// above any real camera photo (a 100 MP phone panorama is the only thing
// that comes close) and far below the point where decoding threatens the
// process.
//
// This exists because image dimensions are declared in a file's header
// and cost nothing to inflate. A "decompression bomb" is a small file
// claiming enormous dimensions: a PNG of a single repeated colour
// compresses to almost nothing, so ~850 KB of upload — well under
// maxUploadFileSize — is enough to declare 30000x30000 and make
// image.Decode try to allocate around a gigabyte in one go. Since
// thumbnails are generated eagerly at upload AND swept again by
// -backfill-thumbnails on every startup, one such file would knock the
// site over repeatedly, for both units, until it was found and removed.
//
// The fix is cheap: image.DecodeConfig reads only the header, so the
// dimensions can be checked before committing to the allocation.
const MaxPixels = 50_000_000

// ErrTooLarge is returned when src's declared dimensions exceed
// MaxPixels. Distinct from ErrNotAnImage because it isn't a "this isn't
// an image" case — it decodes fine, it's just implausibly large, and the
// caller logging it should be able to tell those apart.
var ErrTooLarge = errors.New("thumbnail: image dimensions are too large to process")

// ErrNotAnImage is returned when src can't be decoded as one of the
// image formats this package handles (JPEG, PNG, GIF). Callers should
// fall back to serving the original bytes unchanged — e.g. a
// general-document file that ended up here by mistake, or an image
// format (WebP, HEIC) this package doesn't decode.
var ErrNotAnImage = errors.New("thumbnail: source is not a decodable image")

// Generate decodes src and returns a JPEG-encoded copy scaled down so
// its longest side is at most MaxDimension, preserving aspect ratio.
// Always re-encodes as JPEG — even a source already at or under
// MaxDimension — so a leader who uploads a large lossless PNG (e.g. a
// screenshot) still gets a bandwidth-appropriate thumbnail; only the
// original file's own full-size download ever serves the source bytes
// unchanged.
func Generate(src []byte) ([]byte, error) {
	// Check the header before decoding — see MaxPixels for why.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, ErrNotAnImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrNotAnImage
	}
	// int64 so the multiply can't overflow on a 32-bit build before the
	// comparison gets a chance to reject it.
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, ErrTooLarge
	}

	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, ErrNotAnImage
	}
	// Go's decoder ignores EXIF orientation entirely (decodes the raw
	// pixel grid as stored), unlike every browser rendering the
	// original file directly — apply it ourselves so a photo taken with
	// the phone held sideways still comes out upright.
	img = applyOrientation(img, exifOrientation(src))

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrNotAnImage
	}

	longest := w
	if h > longest {
		longest = h
	}
	scale := 1.0
	if longest > MaxDimension {
		scale = float64(MaxDimension) / float64(longest)
	}
	dstW := max(1, int(float64(w)*scale+0.5))
	dstH := max(1, int(float64(h)*scale+0.5))

	// JPEG has no alpha channel — filling with white first (rather than
	// leaving the canvas at its zero value, transparent black) means a
	// source PNG with a transparent background comes out looking like a
	// photo on white, not one with a black background baked in.
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("thumbnail: encoding: %w", err)
	}
	return buf.Bytes(), nil
}
