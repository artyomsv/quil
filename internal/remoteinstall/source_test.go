package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// releaseServer stands in for GitHub: the release JSON, the platform archive,
// and checksums.txt, wired together the way a real release is.
//
// FetchRelease is otherwise entirely untested — it is the path that actually
// runs in production, and it stitches together three components (Checker,
// Stager, PackDir) whose contract with each other nothing else exercises.
func releaseServer(t *testing.T, version string, p Platform, binaries map[string]string) *httptest.Server {
	t.Helper()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for name, body := range binaries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	assetName := fmt.Sprintf("quil_%s_%s_%s.tar.gz", version, p.GOOS, p.GOARCH)
	sum := sha256.Sum256(archive.Bytes())
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive.Bytes())
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(checksums))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"tag_name": "v%s",
			"assets": [
				{"name": %q, "browser_download_url": %q},
				{"name": "checksums.txt", "browser_download_url": %q}
			]
		}`, version, assetName, srv.URL+"/"+assetName, srv.URL+"/checksums.txt")
	})
	return srv
}

// elfFor builds a body that passes both the format and architecture checks.
func elfFor(p Platform) string {
	machine := uint16(elfMachineAMD64)
	if p.GOARCH == "arm64" {
		machine = elfMachineARM64
	}
	return string(elfHeader(machine, true))
}

func TestFetchRelease_DownloadsVerifiesAndRepacks(t *testing.T) {
	p := Platform{"linux", "arm64"}
	body := elfFor(p)
	srv := releaseServer(t, "1.43.1", p, map[string]string{"quil": body, "quild": body})

	src, err := fetchReleaseFrom(context.Background(), srv.URL, "1.43.1", p)
	if err != nil {
		t.Fatalf("FetchRelease error = %v", err)
	}
	if src.Version != "1.43.1" {
		t.Errorf("Version = %q, want 1.43.1", src.Version)
	}
	if len(src.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want 64 hex characters", src.SHA256)
	}

	// The repacked archive must carry both binaries under their plain names —
	// that is what the remote install script extracts.
	names := archiveNames(t, src.Archive)
	for _, want := range binaryNames {
		if !names[want] {
			t.Errorf("repacked archive is missing %q (has %v)", want, names)
		}
	}

	// The digest must describe what will actually be sent, since the remote
	// re-checks it before installing anything.
	sum := sha256.Sum256(src.Archive)
	if got := hex.EncodeToString(sum[:]); got != src.SHA256 {
		t.Errorf("SHA256 = %s, but the archive hashes to %s", src.SHA256, got)
	}
}

// A release whose archive does not match checksums.txt must not install. The
// verification lives in update.Stager; this pins that FetchRelease actually
// benefits from it rather than bypassing it.
func TestFetchRelease_RejectsChecksumMismatch(t *testing.T) {
	p := Platform{"linux", "amd64"}
	assetName := fmt.Sprintf("quil_1.43.1_%s_%s.tar.gz", p.GOOS, p.GOARCH)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not the archive the checksum describes"))
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), assetName)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v1.43.1","assets":[
			{"name":%q,"browser_download_url":%q},
			{"name":"checksums.txt","browser_download_url":%q}]}`,
			assetName, srv.URL+"/"+assetName, srv.URL+"/checksums.txt")
	})

	if _, err := fetchReleaseFrom(context.Background(), srv.URL, "1.43.1", p); err == nil {
		t.Fatal("error = nil, want the checksum mismatch to be refused")
	}
}

// A release that has no archive for the remote's platform must say so, rather
// than installing whatever else it found.
func TestFetchRelease_RejectsMissingPlatformAsset(t *testing.T) {
	built := Platform{"linux", "amd64"}
	body := elfFor(built)
	srv := releaseServer(t, "1.43.1", built, map[string]string{"quil": body, "quild": body})

	_, err := fetchReleaseFrom(context.Background(), srv.URL, "1.43.1", Platform{"darwin", "arm64"})
	if err == nil {
		t.Fatal("error = nil, want a refusal for an unavailable platform")
	}
	if !strings.Contains(err.Error(), "darwin") {
		t.Errorf("error %q does not name the platform that was unavailable", err)
	}
}

func archiveNames(t *testing.T, gzipped []byte) map[string]bool {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gzipped))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names[hdr.Name] = true
	}
	return names
}

// archiveFiles reads every entry of a tar.gz into name → body.
func archiveFiles(t *testing.T, gzipped []byte) map[string][]byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gzipped))
	if err != nil {
		t.Fatalf("not a gzip stream: %v", err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		files[hdr.Name] = body
	}
}

// fakePE returns a minimal PE image header for machine (0x8664 / 0xAA64).
func fakePE(machine uint16) []byte {
	b := make([]byte, 0x100)
	copy(b, "MZ")
	b[0x3c] = 0x80 // e_lfanew
	copy(b[0x80:], "PE\x00\x00")
	b[0x84] = byte(machine)
	b[0x85] = byte(machine >> 8)
	return b
}

func hexSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestCheckBinaryFormat_Windows(t *testing.T) {
	win := Platform{GOOS: "windows", GOARCH: "amd64"}
	if err := checkBinaryFormat("quil.exe", fakePE(0x8664), win); err != nil {
		t.Errorf("amd64 PE refused: %v", err)
	}
	if err := checkBinaryFormat("quil.exe", fakePE(0xAA64), win); err == nil {
		t.Error("arm64 PE accepted for windows/amd64")
	}
	if err := checkBinaryFormat("quil.exe", []byte("\x7fELF\x02\x01\x01\x00"), win); err == nil {
		t.Error("ELF accepted for windows")
	}
	if err := checkBinaryFormat("quil", fakePE(0x8664), Platform{GOOS: "linux", GOARCH: "amd64"}); err == nil {
		t.Error("PE accepted for linux")
	}
}

// Windows PowerShell started by Win32-OpenSSH cannot read ssh stdin, so the
// archive is extracted by the host's own tar.exe — which reads tar.gz, not
// zip. Each file's own hash rides along because the finalize step verifies
// the EXTRACTED files: tar.exe never sees the archive's digest.
func TestPackDir_Windows_TarGzWithExeNamesAndHashes(t *testing.T) {
	dir := t.TempDir()
	written := map[string][]byte{}
	for i, n := range []string{"quil.exe", "quild.exe", "quil-activate.exe"} {
		body := append(fakePE(0x8664), byte(i)) // distinct bodies, distinct hashes
		written[n] = body
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := PackDir(dir, Platform{GOOS: "windows", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	files := archiveFiles(t, src.Archive)
	names := slices.Sorted(maps.Keys(files))
	if want := []string{"quil-activate.exe", "quil.exe", "quild.exe"}; !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	if keys := slices.Sorted(maps.Keys(src.FileSHA256)); !slices.Equal(keys, names) {
		t.Errorf("FileSHA256 keys = %v, want %v", keys, names)
	}
	for n, body := range written {
		if !bytes.Equal(files[n], body) {
			t.Errorf("%s: archived bytes differ from the file", n)
		}
		if got := src.FileSHA256[n]; got != hexSHA(body) {
			t.Errorf("FileSHA256[%s] = %q, want %q", n, got, hexSHA(body))
		}
	}
	if src.SHA256 != hexSHA(src.Archive) {
		t.Errorf("SHA256 does not describe the archive")
	}
}

func TestPackDir_Windows_ActivateOptional_CrossNamesAccepted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"quil-windows-amd64.exe", "quild-windows-amd64.exe"} {
		if err := os.WriteFile(filepath.Join(dir, n), fakePE(0x8664), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := PackDir(dir, Platform{GOOS: "windows", GOARCH: "amd64"})
	if err != nil {
		t.Fatalf("cross-named binaries refused: %v", err)
	}
	// The entry names are the INSTALLED names, whatever the source spelling.
	if keys := slices.Sorted(maps.Keys(src.FileSHA256)); !slices.Equal(keys, []string{"quil.exe", "quild.exe"}) {
		t.Errorf("FileSHA256 keys = %v, want exactly quil.exe and quild.exe", keys)
	}
}

func TestPackDir_Windows_RequiresQuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quil.exe"), fakePE(0x8664), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PackDir(dir, Platform{GOOS: "windows", GOARCH: "amd64"}); err == nil || !strings.Contains(err.Error(), "quild") {
		t.Errorf("err = %v, want a refusal naming quild", err)
	}
}

// The POSIX archive is what the POSIX install script re-verifies by its own
// digest, so adding the per-file map must not change a single entry.
func TestPackDir_Linux_Unchanged(t *testing.T) {
	dir := t.TempDir()
	body := elfHeader(elfMachineAMD64, true)
	for _, n := range []string{"quil", "quild", "quil-activate"} {
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src, err := PackDir(dir, Platform{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	files := archiveFiles(t, src.Archive)
	if names := slices.Sorted(maps.Keys(files)); !slices.Equal(names, []string{"quil", "quild"}) {
		t.Errorf("entries = %v, want exactly quil and quild", names)
	}
	if keys := slices.Sorted(maps.Keys(src.FileSHA256)); !slices.Equal(keys, []string{"quil", "quild"}) {
		t.Errorf("FileSHA256 keys = %v", keys)
	}
	if src.FileSHA256["quil"] != hexSHA(body) {
		t.Errorf("FileSHA256[quil] = %q", src.FileSHA256["quil"])
	}
}
