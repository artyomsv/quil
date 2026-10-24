package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fetchFixture serves one archive and one checksums body over httptest and
// returns a Release naming them assetName / sumsName. hits counts requests,
// so a test can prove a refusal happened before any download.
func fetchFixture(t *testing.T, archive []byte, assetName, sumsName, sums string) (*Release, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte(sums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{
		TagName: "v0.0.9",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: assetName, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: sumsName, DownloadURL: srv.URL + "/sums"},
		},
	}
	return rel, &hits
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// noDownloadLeftover asserts FetchVerified cleaned its temp file up: a
// refused archive must not sit in the caller's directory looking like one
// that passed.
func noDownloadLeftover(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // never created — nothing to leak
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "download-") {
			t.Errorf("download temp %s left behind after a refusal", e.Name())
		}
	}
}

// TestFetchVerified_PerOSChecksumsLayout: the layout the plugin path meets
// (one checksums file per OS, an archive name template that is not Quil's).
// Both names are the caller's; nothing in here assumes "checksums.txt".
func TestFetchVerified_PerOSChecksumsLayout(t *testing.T) {
	archive := buildZip(t, map[string][]byte{"tool.exe": []byte("fake-tool")})
	const asset = "tool_0.0.9_windows_x86_64.zip"
	const sums = "tool-windows-checksums.txt"
	// A second line with sha256sum's "*" binary marker and an unrelated name,
	// so the parse is shown to pick the right line, not the only line.
	body := fmt.Sprintf("%s *tool_0.0.9_windows_arm64.zip\n%s  %s\n", sha256Hex([]byte("other")), sha256Hex(archive), asset)
	rel, _ := fetchFixture(t, archive, asset, sums, body)
	dst := filepath.Join(t.TempDir(), "dl")

	path, err := FetchVerified(context.Background(), nil, rel, asset, sums, dst)
	if err != nil {
		t.Fatalf("FetchVerified: %v", err)
	}
	if filepath.Dir(path) != dst {
		t.Errorf("archive path = %q, want it under %q", path, dst)
	}
	got, err := os.ReadFile(path)
	if err != nil || sha256Hex(got) != sha256Hex(archive) {
		t.Errorf("downloaded bytes differ from the served archive (err %v)", err)
	}
}

// TestFetchVerified_BinaryModeMarker pins the one parser change made for
// other upstreams: a "<hex> *<name>" line (sha256sum's binary-mode marker)
// names the asset. Quil's own files use the two-space form, which
// stage_test.go's fixtures keep covering.
func TestFetchVerified_BinaryModeMarker(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{"tool": []byte("fake-tool")})
	const asset = "tool_0.0.9_linux_x86_64.tar.gz"
	rel, _ := fetchFixture(t, archive, asset, "tool-linux-checksums.txt", fmt.Sprintf("%s *%s\n", sha256Hex(archive), asset))

	path, err := FetchVerified(context.Background(), nil, rel, asset, "tool-linux-checksums.txt", filepath.Join(t.TempDir(), "dl"))
	if err != nil {
		t.Fatalf("FetchVerified with a *-marked line = %v, want the line to name the asset", err)
	}
	os.Remove(path)
}

func TestFetchVerified_Mismatch(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{"tool": []byte("fake-tool")})
	const asset = "tool_0.0.9_linux_x86_64.tar.gz"
	body := fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset)
	rel, _ := fetchFixture(t, archive, asset, "tool-linux-checksums.txt", body)
	dst := filepath.Join(t.TempDir(), "dl")

	path, err := FetchVerified(context.Background(), nil, rel, asset, "tool-linux-checksums.txt", dst)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchVerified error = %v (path %q), want ErrChecksumMismatch", err, path)
	}
	if !strings.Contains(err.Error(), asset) {
		t.Errorf("error = %q, want it to name the archive", err)
	}
	noDownloadLeftover(t, dst)
}

func TestFetchVerified_NoChecksumLine(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{"tool": []byte("fake-tool")})
	const asset = "tool_0.0.9_linux_x86_64.tar.gz"
	// A well-formed file that lists every OTHER build.
	body := fmt.Sprintf("%s  tool_0.0.9_linux_arm64.tar.gz\n", sha256Hex(archive))
	rel, _ := fetchFixture(t, archive, asset, "tool-linux-checksums.txt", body)
	dst := filepath.Join(t.TempDir(), "dl")

	_, err := FetchVerified(context.Background(), nil, rel, asset, "tool-linux-checksums.txt", dst)
	if !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("FetchVerified error = %v, want ErrNoChecksum", err)
	}
	if !strings.Contains(err.Error(), asset) {
		t.Errorf("error = %q, want it to name the archive", err)
	}
	noDownloadLeftover(t, dst)
}

// TestFetchVerified_NoAsset: either name missing from the release is refused
// BEFORE any request — a release with no build for this platform is answered
// from the JSON already in hand.
func TestFetchVerified_NoAsset(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{"tool": []byte("fake-tool")})
	const asset = "tool_0.0.9_linux_x86_64.tar.gz"
	const sums = "tool-linux-checksums.txt"
	rel, hits := fetchFixture(t, archive, asset, sums, fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset))
	dst := filepath.Join(t.TempDir(), "dl")

	for _, tc := range []struct{ name, wantAsset, wantSums string }{
		{"archive missing", "tool_0.0.9_mac-os_arm64.tar.gz", sums},
		{"checksums missing", asset, "tool-mac-checksums.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FetchVerified(context.Background(), nil, rel, tc.wantAsset, tc.wantSums, dst)
			if !errors.Is(err, ErrNoAsset) {
				t.Fatalf("FetchVerified error = %v, want ErrNoAsset", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a missing asset still reached the network: %d requests", n)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dstDir created for a refusal that needed no I/O, stat err = %v", err)
	}
}

func TestExtractBinary_ZipAndTarGz(t *testing.T) {
	t.Run("zip at root, windows appends .exe", func(t *testing.T) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "tool.zip")
		os.WriteFile(archive, buildZip(t, map[string][]byte{
			"tool.exe": []byte("fake-tool-win"),
			"README":   []byte("x"),
		}), 0600)
		dst := filepath.Join(dir, "out")
		os.Mkdir(dst, 0700)

		got, err := ExtractBinary(archive, "tool", "windows", dst)
		if err != nil {
			t.Fatalf("ExtractBinary: %v", err)
		}
		if got != filepath.Join(dst, "tool.exe") {
			t.Errorf("path = %q, want %q", got, filepath.Join(dst, "tool.exe"))
		}
		if b, _ := os.ReadFile(got); string(b) != "fake-tool-win" {
			t.Errorf("content = %q", b)
		}
		// A name that already carries .exe is not doubled.
		if got2, err := ExtractBinary(archive, "tool.exe", "windows", dst); err != nil || got2 != got {
			t.Errorf("ExtractBinary(tool.exe) = %q, %v; want %q", got2, err, got)
		}
	})

	// Windows filenames are case-insensitive, so an upstream zip spelling the
	// entry "Tool.EXE" must still match — and land under OUR spelling.
	t.Run("zip entry differs in case, windows", func(t *testing.T) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "tool.zip")
		os.WriteFile(archive, buildZip(t, map[string][]byte{"Tool.EXE": []byte("fake-tool-caps")}), 0600)
		dst := filepath.Join(dir, "out")
		os.Mkdir(dst, 0700)

		got, err := ExtractBinary(archive, "tool", "windows", dst)
		if err != nil {
			t.Fatalf("ExtractBinary: %v", err)
		}
		if got != filepath.Join(dst, "tool.exe") {
			t.Errorf("path = %q, want %q (our spelling, not the archive's)", got, filepath.Join(dst, "tool.exe"))
		}
		if b, _ := os.ReadFile(got); string(b) != "fake-tool-caps" {
			t.Errorf("content = %q", b)
		}
		// Not on linux: there "Tool" and "tool" are two files.
		if _, err := ExtractBinary(archive, "tool", "linux", t.TempDir()); err == nil {
			t.Error("ExtractBinary(linux) matched Tool.EXE for tool, want exact-case matching")
		}
	})

	t.Run("tar.gz nested path, linux", func(t *testing.T) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "tool.tar.gz")
		os.WriteFile(archive, buildTarGz(t, map[string][]byte{
			"tool-0.0.9/bin/tool": []byte("fake-tool-linux"),
			"tool-0.0.9/LICENSE":  []byte("mit"),
		}), 0600)
		dst := filepath.Join(dir, "out")
		os.Mkdir(dst, 0700)

		got, err := ExtractBinary(archive, "tool", "linux", dst)
		if err != nil {
			t.Fatalf("ExtractBinary: %v", err)
		}
		if got != filepath.Join(dst, "tool") {
			t.Errorf("path = %q, want %q", got, filepath.Join(dst, "tool"))
		}
		if b, _ := os.ReadFile(got); string(b) != "fake-tool-linux" {
			t.Errorf("content = %q", b)
		}
		if info, err := os.Stat(got); err == nil && info.Mode()&0100 == 0 && os.PathSeparator == '/' {
			t.Errorf("mode = %v, want the owner execute bit", info.Mode())
		}
	})

	t.Run("binary absent", func(t *testing.T) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "tool.tar.gz")
		os.WriteFile(archive, buildTarGz(t, map[string][]byte{"other": []byte("x")}), 0600)
		if _, err := ExtractBinary(archive, "tool", "linux", dir); err == nil {
			t.Error("ExtractBinary for a name the archive lacks = nil error, want error")
		}
	})
}

// TestExtractBinary_RefusesTraversal: an entry named to climb out of dstDir
// lands INSIDE it under the constant base name, and nothing lands outside.
// The guard is structural — the entry path is never joined to anything —
// so the assertion is about the parent directory staying empty.
func TestExtractBinary_RefusesTraversal(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dst, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "evil.tar.gz")
	os.WriteFile(archive, buildTarGz(t, map[string][]byte{
		"../../tool":  []byte("climber"),
		"../../other": []byte("stray"),
	}), 0600)

	got, err := ExtractBinary(archive, "tool", "linux", dst)
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}
	if got != filepath.Join(dst, "tool") {
		t.Errorf("path = %q, want %q (inside dstDir)", got, filepath.Join(dst, "tool"))
	}
	for _, p := range []string{filepath.Join(root, "tool"), filepath.Join(root, "other"),
		filepath.Join(root, "a", "tool"), filepath.Join(dst, "other")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (stat err %v) — an archive path escaped or an unwanted entry was written", p, err)
		}
	}
}
