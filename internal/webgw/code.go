package webgw

import (
	"io"
	"strings"
	"unicode"
)

const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford base32
const codeLen = 10

// newLoginCode draws 10 characters from the Crockford alphabet, about 50
// bits. Each random byte's low five bits pick a character; 256 is a multiple
// of 32, so the choice is unbiased.
func newLoginCode(r io.Reader) (string, error) {
	b := make([]byte, codeLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	out := make([]byte, codeLen)
	for i, v := range b {
		out[i] = codeAlphabet[v&31]
	}
	return string(out), nil
}

// normalizeCode reads a typed code the way Crockford base32 intends: case
// does not matter, I and L mean 1, O means 0, and hyphens and spaces are
// ignored. Anything outside ASCII is kept as it is and never matches: upper
// casing first would fold characters such as dotless i and long s into the
// alphabet.
func normalizeCode(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c < 0x80 {
			c = unicode.ToUpper(c)
		}
		switch c {
		case '-', ' ', '\t':
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// FormatCode shows a code as two groups of five.
func FormatCode(c string) string {
	if len(c) != codeLen {
		return c
	}
	return c[:5] + "-" + c[5:]
}
