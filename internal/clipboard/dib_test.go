package clipboard

import (
	"encoding/binary"
	"image/color"
	"strings"
	"testing"
)

// makeDIB synthesizes a Windows DIB byte slice (BITMAPINFOHEADER + pixel data)
// for the given dimensions, bit count, and pixel filler. Used by the table
// tests below to feed decodeDIB without going through the actual clipboard.
func makeDIB(t *testing.T, width, height int32, bitCount uint16, fill func(x, y int) (r, g, b, a byte)) []byte {
	t.Helper()

	const headerSize = 40
	absHeight := int(height)
	if absHeight < 0 {
		absHeight = -absHeight
	}
	stride := ((int(width)*int(bitCount) + 31) / 32) * 4

	buf := make([]byte, headerSize+stride*absHeight)

	// BITMAPINFOHEADER
	binary.LittleEndian.PutUint32(buf[0:4], headerSize)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(width))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(height)) // negative for top-down
	binary.LittleEndian.PutUint16(buf[12:14], 1)             // planes
	binary.LittleEndian.PutUint16(buf[14:16], bitCount)
	binary.LittleEndian.PutUint32(buf[16:20], biRGB) // compression
	binary.LittleEndian.PutUint32(buf[20:24], uint32(stride*absHeight))
	// remaining fields (pels per meter, color indexes) zeroed by default

	// Pixel data — DIB rows are stored bottom-up when height is positive.
	// Only 24/32bpp are filled; other bit counts get a zeroed buffer (used
	// by the rejection tests, where decodeDIB bails out before reading
	// pixel bytes).
	if bitCount == 24 || bitCount == 32 {
		pixels := buf[headerSize:]
		for y := 0; y < absHeight; y++ {
			var srcY int
			if height > 0 {
				srcY = absHeight - 1 - y // bottom-up
			} else {
				srcY = y
			}
			row := pixels[y*stride:]
			for x := 0; x < int(width); x++ {
				r, g, b, a := fill(x, srcY)
				if bitCount == 32 {
					row[x*4+0] = b
					row[x*4+1] = g
					row[x*4+2] = r
					row[x*4+3] = a
				} else {
					row[x*3+0] = b
					row[x*3+1] = g
					row[x*3+2] = r
				}
			}
		}
	}
	return buf
}

func TestDecodeDIB_32bppBGRA_RoundTrip(t *testing.T) {
	const w, h = 4, 3
	// Each pixel encodes its (x, y) into the color so we can verify ordering.
	fill := func(x, y int) (r, g, b, a byte) {
		return byte(x * 50), byte(y * 70), 200, 255
	}

	dib := makeDIB(t, w, h, 32, fill)
	img, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != w || bounds.Dy() != h {
		t.Fatalf("bounds: got %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			wantR, wantG, wantB, wantA := fill(x, y)
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.R != wantR || c.G != wantG || c.B != wantB || c.A != wantA {
				t.Errorf("pixel (%d,%d) = %v, want NRGBA{%d,%d,%d,%d}",
					x, y, c, wantR, wantG, wantB, wantA)
			}
		}
	}
}

func TestDecodeDIB_24bppBGR_RoundTrip(t *testing.T) {
	const w, h = 5, 2
	fill := func(x, y int) (r, g, b, a byte) {
		return byte(x * 30), byte(y * 100), byte(x*10 + y*5), 255
	}

	dib := makeDIB(t, w, h, 24, fill)
	img, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB: %v", err)
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			wantR, wantG, wantB, _ := fill(x, y)
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.R != wantR || c.G != wantG || c.B != wantB || c.A != 0xFF {
				t.Errorf("pixel (%d,%d) = %v, want NRGBA{%d,%d,%d,255}",
					x, y, c, wantR, wantG, wantB)
			}
		}
	}
}

func TestDecodeDIB_32bppZeroAlphaPromotedToOpaque(t *testing.T) {
	// Some apps leave the 32bpp alpha channel as 0 even though the image
	// is opaque. Quil's decoder should detect "all alpha = 0" and promote
	// to 0xFF, otherwise downstream tools see a fully-transparent image.
	const w, h = 3, 3
	fill := func(x, y int) (r, g, b, a byte) {
		return byte(x * 80), byte(y * 80), 100, 0 // alpha = 0
	}

	dib := makeDIB(t, w, h, 32, fill)
	img, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB: %v", err)
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A != 0xFF {
				t.Errorf("pixel (%d,%d) alpha = %d, want 255 (promoted)", x, y, c.A)
			}
		}
	}
}

func TestDecodeDIB_TopDown(t *testing.T) {
	// Negative height in BITMAPINFOHEADER means rows are stored top-down
	// (the natural reading order). Verify the decoder handles this.
	const w = 2
	height := int32(-3) // top-down, 3 rows
	fill := func(x, y int) (r, g, b, a byte) {
		return byte(y), 0, 0, 255 // red channel encodes Y
	}

	dib := makeDIB(t, w, height, 32, fill)
	img, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB: %v", err)
	}

	for y := 0; y < 3; y++ {
		c := color.NRGBAModel.Convert(img.At(0, y)).(color.NRGBA)
		if int(c.R) != y {
			t.Errorf("row %d: red = %d, want %d (top-down ordering)", y, c.R, y)
		}
	}
}

func TestDecodeDIB_RejectsUnsupported(t *testing.T) {
	cases := []struct {
		name      string
		dib       []byte
		errSubstr string
	}{
		{
			name:      "too small",
			dib:       []byte{0, 0, 0},
			errSubstr: "too small",
		},
		{
			name: "16bpp not supported",
			dib: func() []byte {
				return makeDIB(t, 4, 4, 16, func(int, int) (byte, byte, byte, byte) { return 0, 0, 0, 0 })
			}(),
			errSubstr: "bit count",
		},
		{
			name: "zero width",
			dib: func() []byte {
				dib := makeDIB(t, 1, 1, 32, func(int, int) (byte, byte, byte, byte) { return 0, 0, 0, 0 })
				binary.LittleEndian.PutUint32(dib[4:8], 0) // width = 0
				return dib
			}(),
			errSubstr: "dimensions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeDIB(tc.dib)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

// TestDecodeDIB_RejectsOversizedDimensions verifies that the per-axis cap
// short-circuits before any pixel allocation. We forge a DIB header that
// claims width > maxDIBDimension but provides no pixel data — decodeDIB
// should reject on the dimension check, not OOM trying to slice.
func TestDecodeDIB_RejectsOversizedDimensions(t *testing.T) {
	// 40-byte BITMAPINFOHEADER, width = 17000 (> 16384), height = 1, 32bpp
	dib := make([]byte, 40)
	binary.LittleEndian.PutUint32(dib[0:4], 40) // header size
	binary.LittleEndian.PutUint32(dib[4:8], 17000)
	binary.LittleEndian.PutUint32(dib[8:12], 1)
	binary.LittleEndian.PutUint16(dib[12:14], 1) // planes
	binary.LittleEndian.PutUint16(dib[14:16], 32)
	binary.LittleEndian.PutUint32(dib[16:20], biRGB)

	_, err := decodeDIB(dib)
	if err == nil {
		t.Fatal("expected error for oversized dimensions, got nil")
	}
	if !strings.Contains(err.Error(), "dimensions exceed cap") {
		t.Errorf("error %q should mention dimension cap", err.Error())
	}
}
