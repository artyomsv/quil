package update

import (
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

// The Windows release archive carries a THIRD binary, quil-activate.exe — the
// windowless handler a desktop-toast click runs. It was added to the archive in
// v1.5x and never to the updater, so every in-place `quil update` since has
// installed quil.exe and quild.exe and silently dropped it. An install that
// only ever upgraded (rather than re-extracting the zip) therefore has no
// helper at all, and `quil notify setup` falls back to registering
// `quil.exe activate` — the console binary whose window flash the helper exists
// to remove.
//
// It cannot join BinaryNames: that list is the REQUIRED set, and
// extractBinaries fails the whole stage when the count does not match. Every
// release before the helper existed would then be unstageable, which turns a
// downgrade into a dead end.
func TestOptionalBinaryNames_WindowsOnly(t *testing.T) {
	if got := OptionalBinaryNames("windows"); len(got) != 1 || got[0] != "quil-activate.exe" {
		t.Errorf("OptionalBinaryNames(windows) = %v, want [quil-activate.exe]", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := OptionalBinaryNames(goos); len(got) != 0 {
			t.Errorf("OptionalBinaryNames(%s) = %v, want empty — the helper is Windows-only", goos, got)
		}
	}
}

// stageFixtureWithHelper serves a windows/amd64 zip that DOES carry the
// activation helper, which stageFixture deliberately does not — that one
// doubles as the pre-helper-release case.
func stageFixtureWithHelper(t *testing.T) *Release {
	t.Helper()
	archive := buildZip(t, map[string][]byte{
		"quil.exe":          []byte("fake-quil-binary"),
		"quild.exe":         []byte("fake-quild-binary"),
		"quil-activate.exe": []byte("fake-activate-binary"),
		"LICENSE":           []byte("mit"),
	})
	sum := sha256.Sum256(archive)
	name := "quil_0.0.4_windows_amd64.zip"
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &Release{
		TagName: "v0.0.4",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: name, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: "checksums.txt", DownloadURL: srv.URL + "/sums"},
		},
	}
}

func TestStage_ExtractsActivateHelper(t *testing.T) {
	rel := stageFixtureWithHelper(t)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}

	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage() = %v", err)
	}
	man, dir, err := FindStaged(root)
	if err != nil || man == nil {
		t.Fatalf("FindStaged() = %v, %v", man, err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "quil-activate.exe"))
	if err != nil || string(got) != "fake-activate-binary" {
		t.Fatalf("staged quil-activate.exe = %q (err %v), want the archive's copy — "+
			"without it an in-place update leaves the toast click handler missing", got, err)
	}

	// In the manifest as well as on disk: VerifyStaged hashes every DECLARED
	// file, so a helper extracted but left undeclared would be installed
	// unverified — the exact hole the manifest-coverage check exists to close.
	if _, ok := man.Files["quil-activate.exe"]; !ok {
		t.Errorf("manifest files = %v, want quil-activate.exe covered", man.Files)
	}
	if err := VerifyStaged(dir, man, BinaryNames("windows")); err != nil {
		t.Errorf("VerifyStaged() = %v, want nil", err)
	}
}

// A release published before the helper existed must still stage. The optional
// tier is what makes that true: fold the helper into BinaryNames instead and
// extractBinaries' count check rejects every such archive, so a user on a
// broken version cannot downgrade out of it.
func TestStage_ArchiveWithoutHelper_StillStages(t *testing.T) {
	rel, _ := stageFixture(t, false)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}

	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage() on a pre-helper archive = %v, want nil", err)
	}
	man, dir, err := FindStaged(root)
	if err != nil || man == nil {
		t.Fatalf("FindStaged() = %v, %v", man, err)
	}
	if _, ok := man.Files["quil-activate.exe"]; ok {
		t.Error("manifest declares quil-activate.exe for an archive that has none")
	}
	if err := VerifyStaged(dir, man, BinaryNames("windows")); err != nil {
		t.Errorf("VerifyStaged() = %v, want nil", err)
	}
}

// stageFixtureHelperNoQuild serves an archive holding the OPTIONAL binary and
// only ONE of the two required ones.
func stageFixtureHelperNoQuild(t *testing.T) *Release {
	t.Helper()
	archive := buildZip(t, map[string][]byte{
		"quil.exe":          []byte("fake-quil-binary"),
		"quil-activate.exe": []byte("fake-activate-binary"),
		"LICENSE":           []byte("mit"),
	})
	sum := sha256.Sum256(archive)
	name := "quil_0.0.5_windows_amd64.zip"
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &Release{
		TagName: "v0.0.5",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: name, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: "checksums.txt", DownloadURL: srv.URL + "/sums"},
		},
	}
}

// The split pair must still be caught now that the completeness check counts
// nothing.
//
// This is the failure the per-name check exists for, and it is one a reviewer
// can talk themselves out of: extractBinaries used to compare len(files) to
// len(names), and with an optional entry in the map that comparison passes for
// an archive holding the helper and no quild — two files, two required names,
// no quild anywhere. The result would be a quil.exe installed beside a STALE
// quild.exe, which is the split-version pair swapPair's whole rollback dance
// exists to prevent, arriving by a route that never reaches swapPair at all.
//
// Pinned as a test rather than left to the comment on the check, because the
// tempting "simplification" back to a length compare is invisible in review:
// it is shorter, it reads correctly, and every other test in this file still
// passes.
func TestStage_HelperPresentQuildMissing_Rejected(t *testing.T) {
	rel := stageFixtureHelperNoQuild(t)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}

	err := s.Stage(context.Background(), rel)
	if err == nil {
		t.Fatal("Stage() = nil for an archive with the helper but no quild.exe — " +
			"a length-based completeness check would pass this and install a split pair")
	}
	if !strings.Contains(err.Error(), "quild.exe") {
		t.Errorf("Stage() error = %q, want it to name the missing quild.exe", err)
	}

	// And it must leave NOTHING stageable behind: the manifest is written last
	// precisely so a failed extract cannot be mistaken for a complete stage.
	man, _, ferr := FindStaged(root)
	if ferr != nil {
		t.Fatalf("FindStaged() = %v", ferr)
	}
	if man != nil {
		t.Errorf("FindStaged() returned manifest %+v after a rejected archive, want none", man)
	}
}
