package webgw

import (
	"bytes"
	"strings"
	"testing"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNewLoginCode_ShapeAndAlphabet(t *testing.T) {
	code, err := newLoginCode(bytes.NewReader(bytes.Repeat([]byte{0xff, 0x00, 0x1f, 0x20}, 8)))
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 10 {
		t.Fatalf("len = %d", len(code))
	}
	for _, c := range code {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("%q outside the alphabet", c)
		}
	}
}

// What a person may type for a code: lower case, I or L for 1, O for 0,
// hyphens and spaces anywhere.
func TestNormalizeCode_Crockford(t *testing.T) {
	cases := map[string]string{
		"k7m2q-9txv4":   "K7M2Q9TXV4",
		" K7M2Q 9TXV4 ": "K7M2Q9TXV4",
		"io1l0-OOOOO":   "1011000000",
	}
	for in, want := range cases {
		if got := normalizeCode(in); got != want {
			t.Fatalf("normalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatCode(t *testing.T) {
	if got := FormatCode("K7M2Q9TXV4"); got != "K7M2Q-9TXV4" {
		t.Fatalf("got %q", got)
	}
}

// Upper casing first would fold dotless i and long s into I and S; they must
// stay outside the alphabet.
func TestNormalizeCode_NonASCIIDoesNotFold(t *testing.T) {
	got := normalizeCode("ıſ")
	if strings.ContainsAny(got, "1IS") || got == "" {
		t.Fatalf("normalizeCode folded to %q", got)
	}
	a := "S7M2Q9TXV4"
	if normalizeCode("ſ"+a[1:]) == a {
		t.Fatal("a non-ASCII letter matched an alphabet letter")
	}
}
