package clientauth

import (
	"strings"
	"testing"
)

func TestParseToken(t *testing.T) {
	allUnderscore := "qtk_0a1b2c3d_" + strings.Repeat("_", 42) + "8" // 32 x 0xff, base64url
	tests := []struct {
		name, in, id string
		ok           bool
	}{
		{"known", katToken, katID, true},
		{"secret contains underscore", allUnderscore, "0a1b2c3d", true},
		{"no prefix", strings.TrimPrefix(katToken, "qtk_"), "", false},
		{"upper-case id", "qtk_0A1B2C3D_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", "", false},
		{"short id", "qtk_0a1b2c_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", "", false},
		{"short secret", "qtk_0a1b2c3d_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHg", "", false},
		{"not base64", "qtk_0a1b2c3d_" + strings.Repeat("*", 43), "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseToken(tt.in)
			if (err == nil) != tt.ok || id != tt.id {
				t.Fatalf("ParseToken = %q, %v; want %q ok=%v", id, err, tt.id, tt.ok)
			}
		})
	}
}

func TestNewToken_RoundTrips(t *testing.T) {
	a, aid, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if id, err := ParseToken(a); err != nil || id != aid {
		t.Fatalf("ParseToken(NewToken) = %q, %v; want %q", id, err, aid)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{"": LevelStandard, "read-only": LevelReadOnly, "standard": LevelStandard, "full": LevelFull} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("admin"); err == nil {
		t.Error("ParseLevel accepted an unknown level")
	}
}
