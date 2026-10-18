package clipboard

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestDecodeAppleScriptPNG(t *testing.T) {
	t.Parallel()
	png := append(append([]byte{}, pngMagic...), "IHDR-and-the-rest"...)
	lit := appleDataPrefix + strings.ToUpper(hex.EncodeToString(png)) + appleDataSuffix

	tests := []struct {
		name    string
		out     string
		want    []byte
		wantErr bool
	}{
		{"literal with newline", lit + "\n", png, false},
		{"lowercase hex", appleDataPrefix + hex.EncodeToString(png) + appleDataSuffix, png, false},
		{"no prefix", "89504E47" + appleDataSuffix, nil, true},
		{"no suffix", appleDataPrefix + "89504E47", nil, true},
		{"other class", "«data TIFF4D4D002A»", nil, true},
		{"odd hex", appleDataPrefix + "89504" + appleDataSuffix, nil, true},
		{"not png", appleDataPrefix + "FFD8FFE0" + appleDataSuffix, nil, true},
		{"empty data", appleDataPrefix + appleDataSuffix, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeAppleScriptPNG([]byte(tt.out))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckPNG(t *testing.T) {
	t.Parallel()
	if _, err := checkPNG(nil); !errors.Is(err, ErrNoImage) {
		t.Errorf("empty: err = %v, want ErrNoImage", err)
	}
	if _, err := checkPNG([]byte("plain text")); err == nil || errors.Is(err, ErrNoImage) {
		t.Errorf("non-PNG: err = %v, want a non-ErrNoImage error", err)
	}
	png := append(append([]byte{}, pngMagic...), 'x')
	if got, err := checkPNG(png); err != nil || !bytes.Equal(got, png) {
		t.Errorf("PNG: got %q, %v", got, err)
	}
}

func TestIsNoImageStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		stderr string
		want   bool
	}{
		{"0:29: execution error: Can’t make some data into the expected type. (-1700)\n", true},
		{"Error: target image/png not available\n", true},
		{"Error: Can't open display: (null)\n", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isNoImageStderr(tt.stderr); got != tt.want {
			t.Errorf("isNoImageStderr(%q) = %v, want %v", tt.stderr, got, tt.want)
		}
	}
}
