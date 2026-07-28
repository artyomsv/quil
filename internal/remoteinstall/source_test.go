package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
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
