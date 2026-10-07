package clientauth

import (
	"strings"
	"testing"
)

// katSecret is katToken's secret half.
const katSecret = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// mustNewToken is NewToken for a fixture: a mint failure fails the test
// instead of handing it an empty token.
func mustNewToken(t *testing.T) (token, id string) {
	t.Helper()
	token, id, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return token, id
}

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
		{"no separator", "qtk_0a1b2c3d" + katSecret, "", false},
		{"non-hex id", "qtk_0a1b2c3g_" + katSecret, "", false},
		{"44-character secret", katToken + "A", "", false},
		// The decoder skips CR and LF, so without the exact-length check this
		// 44-byte string decoded to the same 32 bytes as the known token.
		{"newline inside secret", "qtk_0a1b2c3d_" + katSecret[:20] + "\n" + katSecret[20:], "", false},
		{"trailing newline", katToken + "\n", "", false},
		// The last character carries 4 data bits and 2 padding bits; '9'
		// sets a padding bit, a second spelling of the same 32 bytes.
		{"non-canonical trailing bits", katToken[:len(katToken)-1] + "9", "", false},
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
	b, _ := mustNewToken(t)
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
