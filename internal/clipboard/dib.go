package clipboard

import (
	"encoding/binary"
	"fmt"
	"image"
)

// DIB compression constants from wingdi.h
const (
	biRGB       = 0
	biBitfields = 3
)

// maxDIBDimension caps the per-axis size of a DIB we will accept. 16384 is
// generous (covers any modern monitor and reasonable multi-monitor capture)
// but small enough that even a worst-case 32bpp NRGBA allocation
// (16384 * 16384 * 4 ≈ 1 GB) is bounded. The clipboard input itself is also
// capped via maxClipboardImageBytes (64 MB) on the Windows reader side; this
// constant defends against degenerate aspect ratios that would otherwise
// stay under the byte cap but still produce a huge decoded image.
const maxDIBDimension = 16384

// decodeDIB parses a Windows DIB (device-independent bitmap) — as found in
// the clipboard CF_DIB format — and returns a Go image.Image. It supports
// the common formats produced by Windows screenshot tools (Snipping Tool,
// Win+Shift+S): 24bpp BI_RGB and 32bpp BI_RGB or BI_BITFIELDS with default
// BGRA masks. Other formats return an error.
//
// The DIB layout is:
//
//	BITMAPINFOHEADER (40 bytes) or BITMAPV4/V5HEADER (108 / 124 bytes)
//	  [optional 12 bytes of color masks if BI_BITFIELDS with V1 header]
//	  [optional palette — not present for ≥16bpp BI_RGB]
//	  pixel data, rows packed bottom-up (or top-down if height is negative)
//
// This function lives outside the platform-specific file so the parser can
// be unit-tested on any platform (the only Windows-specific part is reading
// the bytes from the OS clipboard, which is in image_windows.go).
func decodeDIB(data []byte) (image.Image, error) {
	const headerMin = 40
	if len(data) < headerMin {
		return nil, fmt.Errorf("too small (%d bytes, need ≥%d)", len(data), headerMin)
	}

	headerSize := binary.LittleEndian.Uint32(data[0:4])
	if uint32(len(data)) < headerSize {
		return nil, fmt.Errorf("header truncated (%d < %d)", len(data), headerSize)
	}
	if headerSize < headerMin {
		return nil, fmt.Errorf("invalid header size %d", headerSize)
	}

	width := int32(binary.LittleEndian.Uint32(data[4:8]))
	height := int32(binary.LittleEndian.Uint32(data[8:12]))
	bitCount := binary.LittleEndian.Uint16(data[14:16])
	compression := binary.LittleEndian.Uint32(data[16:20])

	if width <= 0 || height == 0 {
		return nil, fmt.Errorf("invalid dimensions: %dx%d", width, height)
	}
	if compression != biRGB && compression != biBitfields {
		return nil, fmt.Errorf("unsupported compression %d (only BI_RGB and BI_BITFIELDS)", compression)
	}
	if bitCount != 24 && bitCount != 32 {
		return nil, fmt.Errorf("unsupported bit count %d (only 24 and 32)", bitCount)
	}

	// Compute pixel data offset.
	//
	// For BITMAPINFOHEADER (size==40) with BI_BITFIELDS, three DWORD color
	// masks live at offset 40-51. For BITMAPV4HEADER (108) and
	// BITMAPV5HEADER (124), the masks are inside the header itself, so we
	// just skip past it.
	pixelOffset := headerSize
	if compression == biBitfields && headerSize == headerMin {
		pixelOffset += 12
		if uint32(len(data)) < pixelOffset {
			return nil, fmt.Errorf("color mask truncated")
		}
	}

	absHeight := int(height)
	topDown := false
	if height < 0 {
		absHeight = int(-height)
		topDown = true
	}

	// Per-axis sanity cap. Defends against crafted DIBs whose total byte
	// count slips under the 64 MB clipboard limit but whose decoded NRGBA
	// allocation (4 bytes/pixel) would still be enormous.
	if int(width) > maxDIBDimension || absHeight > maxDIBDimension {
		return nil, fmt.Errorf("dimensions exceed cap: %dx%d (max %d)",
			width, absHeight, maxDIBDimension)
	}

	// Row stride: each row is padded to a 4-byte boundary. Use uint64 for
	// the intermediate so a degenerate (width, bitCount) pair cannot wrap
	// on a 32-bit build. With the dimension cap above, every value here
	// fits comfortably in a uint64, and we re-validate that the pixel-data
	// extent is representable as int before slicing.
	strideU := ((uint64(width)*uint64(bitCount) + 31) / 32) * 4
	expectedU := uint64(pixelOffset) + strideU*uint64(absHeight)
	if expectedU > uint64(len(data)) {
		return nil, fmt.Errorf("pixel data truncated (have %d, need %d)", len(data), expectedU)
	}
	// Past this point we know expectedU ≤ len(data) which fits in int on
	// every supported arch (Go's int is ≥32 bits and len() returns int).
	stride := int(strideU)
	expected := int(expectedU)
	pixels := data[pixelOffset:expected]

	w, h := int(width), absHeight
	img := image.NewNRGBA(image.Rect(0, 0, w, h))

	// First pass for 32bpp: detect "all alpha = 0" DIBs (some apps leave
	// the alpha channel uninitialized even though the image is opaque).
	// If we don't promote those to 0xFF, the resulting PNG is fully
	// transparent and downstream tools see a blank image.
	alphaIsValid := false
	if bitCount == 32 {
		for y := 0; y < h && !alphaIsValid; y++ {
			row := pixels[y*stride:]
			for x := 0; x < w; x++ {
				if row[x*4+3] != 0 {
					alphaIsValid = true
					break
				}
			}
		}
	}

	for y := 0; y < h; y++ {
		var srcRow int
		if topDown {
			srcRow = y
		} else {
			srcRow = h - 1 - y
		}
		src := pixels[srcRow*stride:]
		dst := img.Pix[y*img.Stride:]

		if bitCount == 32 {
			for x := 0; x < w; x++ {
				b := src[x*4+0]
				g := src[x*4+1]
				r := src[x*4+2]
				a := src[x*4+3]
				if !alphaIsValid {
					a = 0xFF
				}
				dst[x*4+0] = r
				dst[x*4+1] = g
				dst[x*4+2] = b
				dst[x*4+3] = a
			}
		} else { // 24bpp
			for x := 0; x < w; x++ {
				b := src[x*3+0]
				g := src[x*3+1]
				r := src[x*3+2]
				dst[x*4+0] = r
				dst[x*4+1] = g
				dst[x*4+2] = b
				dst[x*4+3] = 0xFF
			}
		}
	}

	return img, nil
}
