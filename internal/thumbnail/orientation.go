package thumbnail

import (
	"encoding/binary"
	"image"
	"image/color"
)

// exifOrientation scans src — the raw bytes of a source file, before
// decoding — for a JPEG EXIF Orientation tag and returns its value
// (1-8, per the TIFF/EXIF spec) if found. Returns 1 ("already upright,
// no correction needed") for anything that isn't a JPEG, carries no
// EXIF APP1 segment, or fails to parse — a photo with no discoverable
// orientation tag is assumed to need no rotation, the same as a
// browser's own fallback.
//
// This exists because Go's stdlib image/jpeg decoder ignores EXIF
// orientation entirely, decoding the raw pixel grid exactly as stored.
// A phone camera held sideways relies on this tag, not the pixel data
// itself, to display upright — every browser honors it when showing the
// original file directly, so skipping it here would silently rotate a
// photo relative to how it always looked before it had a thumbnail.
func exifOrientation(src []byte) int {
	if len(src) < 4 || src[0] != 0xFF || src[1] != 0xD8 {
		return 1
	}
	pos := 2
	for pos+4 <= len(src) {
		if src[pos] != 0xFF {
			return 1
		}
		marker := src[pos+1]
		switch {
		case marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9):
			// No-length markers (SOI, TEM, restart markers): skip just
			// the two marker bytes themselves.
			pos += 2
			continue
		case marker == 0xDA:
			// Start of Scan: compressed image data follows, and EXIF
			// (like every other header segment) always appears before
			// it — nothing left worth scanning.
			return 1
		}

		segLen := int(src[pos+2])<<8 | int(src[pos+3])
		if segLen < 2 || pos+2+segLen > len(src) {
			return 1
		}
		payload := src[pos+4 : pos+2+segLen]
		if marker == 0xE1 && len(payload) >= 6 && string(payload[:6]) == "Exif\x00\x00" {
			if o := parseExifOrientation(payload[6:]); o != 0 {
				return o
			}
			return 1
		}
		pos += 2 + segLen
	}
	return 1
}

// parseExifOrientation reads the Orientation tag (0x0112) out of tiff, a
// TIFF-formatted EXIF block (starting at its own byte-order header, not
// including the "Exif\0\0" prefix). Returns 0 if the block is malformed
// or has no Orientation entry, distinct from the "1 = normal" a real tag
// can carry.
func parseExifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	ifdOffset := int(bo.Uint32(tiff[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(tiff) {
		return 0
	}
	numEntries := int(bo.Uint16(tiff[ifdOffset : ifdOffset+2]))
	entriesStart := ifdOffset + 2
	for i := 0; i < numEntries; i++ {
		entryStart := entriesStart + i*12
		if entryStart+12 > len(tiff) {
			break
		}
		entry := tiff[entryStart : entryStart+12]
		const orientationTag = 0x0112
		const shortType = 3
		if bo.Uint16(entry[0:2]) != orientationTag || bo.Uint16(entry[2:4]) != shortType {
			continue
		}
		value := int(bo.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return 0
		}
		return value
	}
	return 0
}

// orientedImage lazily remaps coordinates so Bounds/At already reflect
// an EXIF orientation correction, avoiding a full extra pixel copy
// before the resize pass in Generate.
type orientedImage struct {
	src         image.Image
	orientation int
	srcMin      image.Point
	w, h        int // source width/height, pre-correction
}

// applyOrientation wraps img so it reads as already rotated/flipped
// upright per orientation (1-8, see exifOrientation) — orientation 1
// (or anything out of range) returns img unchanged, since there's
// nothing to correct.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	return &orientedImage{src: img, orientation: orientation, srcMin: b.Min, w: b.Dx(), h: b.Dy()}
}

func (o *orientedImage) ColorModel() color.Model { return o.src.ColorModel() }

func (o *orientedImage) Bounds() image.Rectangle {
	switch o.orientation {
	case 5, 6, 7, 8:
		return image.Rect(0, 0, o.h, o.w)
	default:
		return image.Rect(0, 0, o.w, o.h)
	}
}

// At maps a destination pixel back to its source pixel for each of the
// 8 EXIF orientations. Source width/height are o.w/o.h throughout,
// keeping every formula below in terms of the pre-correction image
// regardless of which way this particular orientation swaps dimensions.
func (o *orientedImage) At(x, y int) color.Color {
	w, h := o.w, o.h
	var sx, sy int
	switch o.orientation {
	case 2: // mirrored horizontally
		sx, sy = w-1-x, y
	case 3: // rotated 180
		sx, sy = w-1-x, h-1-y
	case 4: // mirrored vertically
		sx, sy = x, h-1-y
	case 5: // transposed (mirrored across the top-left/bottom-right diagonal)
		sx, sy = y, x
	case 6: // rotated 90 clockwise
		sx, sy = y, h-1-x
	case 7: // transverse (mirrored across the other diagonal)
		sx, sy = w-1-y, h-1-x
	case 8: // rotated 90 counterclockwise
		sx, sy = w-1-y, x
	default:
		sx, sy = x, y
	}
	return o.src.At(o.srcMin.X+sx, o.srcMin.Y+sy)
}
