package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildZip returns an in-memory zip holding the given files.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// stageFixture serves a release archive + checksums over httptest and
// returns the Release pointing at it.
func stageFixture(t *testing.T, tamperChecksum bool) (*Release, *httptest.Server) {
	t.Helper()
	archive := buildZip(t, map[string][]byte{
		"quil.exe":  []byte("fake-quil-binary"),
		"quild.exe": []byte("fake-quild-binary"),
		"LICENSE":   []byte("mit"),
	})
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	if tamperChecksum {
		hexSum = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	name := "quil_0.0.2_windows_amd64.zip"
	checksums := fmt.Sprintf("%s  %s\n", hexSum, name)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{
		TagName: "v0.0.2",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: name, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: "checksums.txt", DownloadURL: srv.URL + "/sums"},
		},
	}
	return rel, srv
}

// buildTarGz returns an in-memory gzip'd tar holding the given files — the
// archive format for 4 of quil's 5 release targets (linux/amd64,
// linux/arm64, darwin/amd64, darwin/arm64; only windows/amd64 ships a zip).
func buildTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// stageFixtureTarGz mirrors stageFixture but serves a linux/amd64 tar.gz
// release — the tar-extraction path in extractBinaries is otherwise
// completely untested (the zip-only stageFixture exercises the zip branch
// only).
func stageFixtureTarGz(t *testing.T) *Release {
	t.Helper()
	archive := buildTarGz(t, map[string][]byte{
		"quil":    []byte("fake-quil-binary"),
		"quild":   []byte("fake-quild-binary"),
		"LICENSE": []byte("mit"),
	})
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	name := "quil_0.0.3_linux_amd64.tar.gz"
	checksums := fmt.Sprintf("%s  %s\n", hexSum, name)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &Release{
		TagName: "v0.0.3",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: name, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: "checksums.txt", DownloadURL: srv.URL + "/sums"},
		},
	}
}

func TestStage_TarGzHappyPath_WritesBinariesAndManifest(t *testing.T) {
	rel := stageFixtureTarGz(t)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "linux", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	dir := filepath.Join(root, "staged", "0.0.3")
	for _, name := range []string{"quil", "quild", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("staged file %s missing: %v", name, err)
		}
	}
	man, gotDir, err := FindStaged(root)
	if err != nil || man == nil {
		t.Fatalf("FindStaged: man=%v err=%v", man, err)
	}
	if man.Version != "0.0.3" || gotDir != dir {
		t.Errorf("FindStaged = (%q, %q), want (0.0.3, %q)", man.Version, gotDir, dir)
	}
	if err := VerifyStaged(dir, man); err != nil {
		t.Errorf("VerifyStaged on fresh tar.gz stage: %v", err)
	}
}

func TestStage_HappyPath_WritesBinariesAndManifest(t *testing.T) {
	rel, _ := stageFixture(t, false)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	dir := filepath.Join(root, "staged", "0.0.2")
	for _, name := range []string{"quil.exe", "quild.exe", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("staged file %s missing: %v", name, err)
		}
	}
	man, gotDir, err := FindStaged(root)
	if err != nil || man == nil {
		t.Fatalf("FindStaged: man=%v err=%v", man, err)
	}
	if man.Version != "0.0.2" || gotDir != dir {
		t.Errorf("FindStaged = (%q, %q), want (0.0.2, %q)", man.Version, gotDir, dir)
	}
	if err := VerifyStaged(dir, man); err != nil {
		t.Errorf("VerifyStaged on fresh stage: %v", err)
	}
}

func TestStage_BadChecksum_NoManifest(t *testing.T) {
	rel, _ := stageFixture(t, true)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err == nil {
		t.Fatal("Stage with tampered checksum = nil error, want error")
	}
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.2", "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("manifest exists after failed stage (err=%v) — atomicity broken", err)
	}
	if man, _, _ := FindStaged(root); man != nil {
		t.Errorf("FindStaged after failed stage = %+v, want nil", man)
	}
}

func TestVerifyStaged_DetectsCorruption(t *testing.T) {
	rel, _ := stageFixture(t, false)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	man, dir, _ := FindStaged(root)
	if err := os.WriteFile(filepath.Join(dir, "quil.exe"), []byte("corrupted"), 0700); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if err := VerifyStaged(dir, man); err == nil {
		t.Error("VerifyStaged on corrupted file = nil error, want error")
	}
}

// TestStage_HostileTagRejected asserts a release tag crafted to escape the
// staging root via its version string ("v1.0.0-../../evil" — the "-" suffix
// is stripped by version parsing elsewhere, but Version() here does not
// re-validate it) is rejected before any asset lookup or network I/O, and
// nothing is created outside <root>/staged/.
func TestStage_HostileTagRejected(t *testing.T) {
	rel := &Release{TagName: "v1.99.0-../../evil"} // no assets — a lookup would fail loudly if reached
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}

	err := s.Stage(context.Background(), rel)
	if err == nil {
		t.Fatal("Stage with hostile tag = nil error, want error")
	}
	if !strings.Contains(err.Error(), "not a safe version") {
		t.Errorf("Stage error = %q, want mention of \"not a safe version\"", err)
	}

	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("ReadDir(root): %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("root dir has entries after rejected stage: %v", entries)
	}
}

func TestPruneStaged_KeepsOnlyGiven(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"0.0.1", "0.0.2"} {
		dir := filepath.Join(root, "staged", v)
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"`+v+`"}`), 0600)
	}
	PruneStaged(root, "0.0.2")
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.1")); !os.IsNotExist(err) {
		t.Error("0.0.1 not pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.2")); err != nil {
		t.Errorf("0.0.2 was pruned: %v", err)
	}
}
