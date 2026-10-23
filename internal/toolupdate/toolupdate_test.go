package toolupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/update"
)

const (
	testRepo = "stripe/stripe-cli"
	testTag  = "v1.55.1"
	testSums = "stripe-checksums.txt"
)

// binName is the file name a tool's binary has on the host running the
// test: the archive is built for runtime.GOOS and extracted the way
// production would on this machine.
func binName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// assetName names the archive the way an upstream does per OS.
func assetName() string {
	if runtime.GOOS == "windows" {
		return "stripe_1.55.1_windows_x86_64.zip"
	}
	return "stripe_1.55.1_linux_x86_64.tar.gz"
}

// buildArchive packs files in the format ExtractBinary expects for
// runtime.GOOS (zip on windows, tar.gz elsewhere). Every entry is 0755 so a
// unix test can extract an executable script.
func buildArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// github is an httptest stand-in for the GitHub release API: one release
// under testTag, its assets served from /dl/<name>, 404 for every other
// tag. latestHits counts /releases/latest, which Run must never request;
// hits counts everything, so a refusal can be shown to precede all I/O.
type github struct {
	srv        *httptest.Server
	hits       atomic.Int32
	latestHits atomic.Int32
}

func newGitHub(t *testing.T, assets map[string][]byte) *github {
	t.Helper()
	gh := &github{}
	release := func(r *http.Request) []byte {
		rel := update.Release{TagName: testTag, URL: "https://example.invalid/rel"}
		for name := range assets {
			rel.Assets = append(rel.Assets, update.Asset{
				Name:        name,
				DownloadURL: "http://" + r.Host + "/dl/" + name,
				Size:        int64(len(assets[name])),
			})
		}
		body, err := json.Marshal(rel)
		if err != nil {
			t.Errorf("marshal release: %v", err) // handler goroutine: Fatalf is not allowed here
		}
		return body
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+testRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		gh.hits.Add(1)
		gh.latestHits.Add(1)
		w.Write(release(r))
	})
	mux.HandleFunc("/repos/"+testRepo+"/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		gh.hits.Add(1)
		if strings.TrimPrefix(r.URL.Path, "/repos/"+testRepo+"/releases/tags/") != testTag {
			http.NotFound(w, r)
			return
		}
		w.Write(release(r))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		gh.hits.Add(1)
		body, ok := assets[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	gh.srv = httptest.NewServer(mux)
	t.Cleanup(gh.srv.Close)
	return gh
}

// installedTool writes a fake installed binary and returns its path, with
// symlinks resolved the way Run resolves them (macOS's temp dir is a link),
// so the printed lines can be matched against it.
func installedTool(t *testing.T, content []byte) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), binName("stripe"))
	if err := os.WriteFile(target, content, 0755); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	return target
}

func options(target string, gh *github) Options {
	return Options{
		Target:    target,
		Repo:      testRepo,
		Tag:       testTag,
		Asset:     assetName(),
		Checksums: testSums,
		Binary:    "stripe",
		GOOS:      runtime.GOOS,
		BaseURL:   gh.srv.URL,
	}
}

// sumsFor builds the checksums body an upstream publishes, with an
// unrelated line first so the lookup is shown to pick by name.
func sumsFor(archive []byte, name string) []byte {
	return []byte(fmt.Sprintf("%s  stripe_1.55.1_linux_arm64.tar.gz\n%s  %s\n", sha256Hex([]byte("other")), sha256Hex(archive), name))
}

// dirEntries lists the names in the target's directory, so a test can prove
// the staging dir and the download temp were cleaned up.
func dirEntries(t *testing.T, target string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// litter plants what a killed earlier run leaves beside target: a stale
// backup and an hours-old staging dir with a half-downloaded archive in it.
func litter(t *testing.T, target string) {
	t.Helper()
	if err := os.WriteFile(target+".old", []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	stagingDir(t, filepath.Dir(target), "stale", -2*time.Hour)
}

// stagingDir creates a staging dir named stagingPrefix+suffix holding a
// partial download, with its mtime shifted by age, and returns its path.
func stagingDir(t *testing.T, dir, suffix string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, stagingPrefix+suffix)
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "download-1"), []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSweepStaging_AgeRule: two panes on one binary can run an update at
// once, so the sweep takes only entries old enough to belong to a run that
// is gone — a fresh staging dir or probe file is someone's live download.
func TestSweepStaging_AgeRule(t *testing.T) {
	dir := t.TempDir()
	old := stagingDir(t, dir, "old", -2*time.Hour)
	fresh := stagingDir(t, dir, "fresh", 0)
	oldProbe := filepath.Join(dir, stagingPrefix+"probe-old")
	freshProbe := filepath.Join(dir, stagingPrefix+"probe-fresh")
	for _, p := range []string{oldProbe, freshProbe} {
		if err := os.WriteFile(p, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldProbe, long, long); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(user, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}

	sweepStaging(dir, time.Now())

	for _, gone := range []string{old, oldProbe} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should have been swept: %v", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{fresh, filepath.Join(fresh, "download-1"), freshProbe, user} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should have survived the sweep: %v", filepath.Base(kept), err)
		}
	}
}

func TestRun_SuccessSwapsAndPrints(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	// Leftovers of an earlier run are swept once the refusals are past, so
	// the backup slot is free for this run and the dir does not fill up.
	litter(t, target)
	archive := buildArchive(t, map[string][]byte{
		"stripe-dir/" + binName("stripe"): []byte("new-bytes"),
		"stripe-dir/LICENSE":              []byte("mit"),
	})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, assetName())})

	var out bytes.Buffer
	if err := Run(context.Background(), options(target, gh), &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}

	if got := readFile(t, target); got != "new-bytes" {
		t.Errorf("target = %q, want new bytes", got)
	}
	if got := readFile(t, target+".old"); got != "old-bytes" {
		t.Errorf("backup = %q, want the pre-update binary (stale backup swept first)", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Errorf("target mode %v is not executable", fi.Mode())
		}
	}
	if names := dirEntries(t, target); len(names) != 2 {
		t.Errorf("target dir holds %v, want only the binary and its backup (stale and own staging dirs removed)", names)
	}
	if gh.latestHits.Load() != 0 {
		t.Errorf("/releases/latest was requested %d times; the update must fetch by tag only", gh.latestHits.Load())
	}

	text := out.String()
	for _, want := range []string{
		"→ downloading " + assetName(),
		"✓ checksum ok",
		"→ replacing " + target,
		"updated " + target + ": unknown → unknown",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

// TestRun_DetectPrintsOldAndNew runs a real detect command through the
// swap: the installed script reports one version, the extracted one
// another, and the final line names both. Unix only — the script needs a
// shebang.
func TestRun_DetectPrintsOldAndNew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs an executable shell script as the target")
	}
	script := func(v string) []byte { return []byte("#!/bin/sh\necho \"stripe version " + v + "\"\n") }
	target := installedTool(t, script("1.31.1"))
	archive := buildArchive(t, map[string][]byte{"stripe": script("1.55.1")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, assetName())})
	o := options(target, gh)
	o.DetectCmd = "stripe --version"

	var out bytes.Buffer
	if err := Run(context.Background(), o, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if want := "updated " + target + ": stripe version 1.31.1 → stripe version 1.55.1"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

// TestRun_DetectFailureIsNotFatal: a target that cannot run its detect
// command (here: not a program at all) still updates; the version reads
// "unknown" and the failure is one printed line.
func TestRun_DetectFailureIsNotFatal(t *testing.T) {
	target := installedTool(t, []byte("not a program"))
	archive := buildArchive(t, map[string][]byte{binName("stripe"): []byte("still not a program")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, assetName())})
	o := options(target, gh)
	o.DetectCmd = "stripe --version"

	var out bytes.Buffer
	if err := Run(context.Background(), o, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if got := readFile(t, target); got != "still not a program" {
		t.Errorf("target = %q, want the new bytes", got)
	}
	if !strings.Contains(out.String(), "! detect") || !strings.Contains(out.String(), "→ current version: unknown") {
		t.Errorf("output should report the detect failure and an unknown version:\n%s", out.String())
	}
}

func TestRun_RefusesSystemLocation(t *testing.T) {
	gh := newGitHub(t, nil)
	o := options("/usr/bin/stripe", gh)
	o.GOOS = "linux"

	var out bytes.Buffer
	err := Run(context.Background(), o, &out)
	if err == nil {
		t.Fatal("Run succeeded on /usr/bin/stripe")
	}
	if !strings.Contains(err.Error(), "installed by a package manager Quil does not know") || !strings.Contains(err.Error(), "/usr/bin/stripe") {
		t.Errorf("error = %q, want the package-manager refusal naming the path", err)
	}
	if gh.hits.Load() != 0 {
		t.Errorf("GitHub was contacted %d times before the refusal", gh.hits.Load())
	}
}

// TestRun_RefusalDeletesNothing: a refused target keeps its backups and
// whatever else sits beside it. The windows arm of the guard is pointed at
// a real temp dir through the injected environment, so the test runs on
// Linux CI with a directory it may actually write to — which is what makes
// "nothing was deleted" observable.
func TestRun_RefusalDeletesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Stripe"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "Stripe", binName("stripe"))
	if err := os.WriteFile(target, []byte("managed"), 0755); err != nil {
		t.Fatal(err)
	}
	litter(t, target)
	before := dirEntries(t, target)

	gh := newGitHub(t, nil)
	o := options(target, gh)
	o.GOOS = "windows"
	o.Env = func(k string) string {
		if k == "ProgramFiles" {
			return root
		}
		return ""
	}

	err := Run(context.Background(), o, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "installed by a package manager Quil does not know") {
		t.Fatalf("err = %v, want the package-manager refusal", err)
	}
	if after := dirEntries(t, target); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("refusal changed the target dir: before %v, after %v", before, after)
	}
	if got := readFile(t, target+".old"); got != "stale" {
		t.Errorf("backup beside a refused target = %q, want untouched", got)
	}
	if gh.hits.Load() != 0 {
		t.Errorf("GitHub was contacted %d times before the refusal", gh.hits.Load())
	}
}

// TestRun_RefusesSystemLocation_Windows drives the windows arm of the guard
// through Run on any host, with the environment injected.
func TestRun_RefusesSystemLocation_Windows(t *testing.T) {
	gh := newGitHub(t, nil)
	o := options(`C:\Program Files\Stripe\stripe.exe`, gh)
	o.GOOS = "windows"
	o.Env = func(k string) string {
		if k == "ProgramFiles" {
			return `C:\Program Files`
		}
		return ""
	}

	err := Run(context.Background(), o, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "installed by a package manager Quil does not know") {
		t.Fatalf("err = %v, want the package-manager refusal", err)
	}
	if gh.hits.Load() != 0 {
		t.Errorf("GitHub was contacted %d times before the refusal", gh.hits.Load())
	}
}

func TestRun_ChecksumMismatchLeavesTarget(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	archive := buildArchive(t, map[string][]byte{binName("stripe"): []byte("new-bytes")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor([]byte("tampered"), assetName())})

	var out bytes.Buffer
	err := Run(context.Background(), options(target, gh), &out)
	if !errors.Is(err, update.ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if got := readFile(t, target); got != "old-bytes" {
		t.Errorf("target = %q after a refused download, want untouched", got)
	}
	if names := dirEntries(t, target); len(names) != 1 {
		t.Errorf("target dir holds %v, want only the untouched binary (no backup, no staging leftovers)", names)
	}
	if strings.Contains(out.String(), "✓ checksum ok") || strings.Contains(out.String(), "replacing") {
		t.Errorf("output claims progress past the refusal:\n%s", out.String())
	}
}

func TestRun_NoChecksumEntryLeavesTarget(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	archive := buildArchive(t, map[string][]byte{binName("stripe"): []byte("new-bytes")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, "some_other_asset.tar.gz")})

	err := Run(context.Background(), options(target, gh), new(bytes.Buffer))
	if !errors.Is(err, update.ErrNoChecksum) {
		t.Fatalf("err = %v, want ErrNoChecksum", err)
	}
	if got := readFile(t, target); got != "old-bytes" {
		t.Errorf("target = %q, want untouched", got)
	}
}

func TestRun_NoAssetLeavesTarget(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	gh := newGitHub(t, map[string][]byte{testSums: []byte("")})

	err := Run(context.Background(), options(target, gh), new(bytes.Buffer))
	if !errors.Is(err, update.ErrNoAsset) {
		t.Fatalf("err = %v, want ErrNoAsset", err)
	}
	if got := readFile(t, target); got != "old-bytes" {
		t.Errorf("target = %q, want untouched", got)
	}
}

// TestRun_MissingTagFailsNoLatestFallback: the dialog offered one version;
// when that tag is gone the update fails rather than installing whatever is
// newest. /releases/latest would have answered — it is never asked.
func TestRun_MissingTagFailsNoLatestFallback(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	archive := buildArchive(t, map[string][]byte{binName("stripe"): []byte("new-bytes")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, assetName())})
	o := options(target, gh)
	o.Tag = "v9.9.9"

	err := Run(context.Background(), o, new(bytes.Buffer))
	if !errors.Is(err, update.ErrReleaseNotFound) {
		t.Fatalf("err = %v, want ErrReleaseNotFound", err)
	}
	if !strings.Contains(err.Error(), "release tag v9.9.9 not found") {
		t.Errorf("error = %q, want it to name the missing tag", err)
	}
	if gh.latestHits.Load() != 0 {
		t.Errorf("/releases/latest was requested %d times after the tag was missing", gh.latestHits.Load())
	}
	if got := readFile(t, target); got != "old-bytes" {
		t.Errorf("target = %q, want untouched", got)
	}
}

// TestRun_MissingTargetRestoredFromBackup: a run killed between its
// rename-aside and its copy leaves the target path empty and the binary in
// "<target>.old". The next run restores it before anything else looks for
// it — a refusal on "no such file" beside the only copy would strand the
// tool — and then updates it.
func TestRun_MissingTargetRestoredFromBackup(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	if err := os.Rename(target, target+".old"); err != nil {
		t.Fatal(err)
	}
	archive := buildArchive(t, map[string][]byte{binName("stripe"): []byte("new-bytes")})
	gh := newGitHub(t, map[string][]byte{assetName(): archive, testSums: sumsFor(archive, assetName())})

	var out bytes.Buffer
	if err := Run(context.Background(), options(target, gh), &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if got := readFile(t, target); got != "new-bytes" {
		t.Errorf("target = %q, want new bytes", got)
	}
	if got := readFile(t, target+".old"); got != "old-bytes" {
		t.Errorf("backup = %q, want the restored binary renamed aside again", got)
	}
	if !strings.Contains(out.String(), "restored "+filepath.Base(target)) {
		t.Errorf("output does not report the restore:\n%s", out.String())
	}
}

// TestRun_EmptyTagRefusedBeforeIO: an empty tag is exactly what
// Checker.Release turns into /releases/latest, so it is refused up front.
func TestRun_EmptyTagRefusedBeforeIO(t *testing.T) {
	target := installedTool(t, []byte("old-bytes"))
	gh := newGitHub(t, nil)
	o := options(target, gh)
	o.Tag = ""

	err := Run(context.Background(), o, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("err = %v, want the missing --tag refusal", err)
	}
	if gh.hits.Load() != 0 {
		t.Errorf("GitHub was contacted %d times with no tag", gh.hits.Load())
	}
	if _, err := os.Stat(target + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup exists after a refusal that touched nothing: %v", err)
	}
}

// TestFirstLine_DropsControlsAndBidi: the detected version is printed into
// the pane and shown by the daemon, so C0/C1 controls and the printable bidi
// overrides are removed while ordinary non-ASCII survives. Runes are built
// numerically — a \u escape in source would land here as the raw rune.
func TestFirstLine_DropsControlsAndBidi(t *testing.T) {
	in := "\r\n  " + string([]rune{0x1b}) + "[1mstripe" + string([]rune{0x9b}) + " version" +
		string([]rune{0x202e}) + " 1.55.1" + string([]rune{0x2066, 0x200f, 0x7f}) + " é\r\nsecond\n"
	want := "[1mstripe version 1.55.1 é"
	if got := firstLine([]byte(in)); got != want {
		t.Errorf("firstLine = %q, want %q", got, want)
	}
	if got := firstLine([]byte("\n\n   \n")); got != "" {
		t.Errorf("firstLine(blank) = %q, want empty", got)
	}
}

func TestRun_MissingTargetFails(t *testing.T) {
	gh := newGitHub(t, nil)
	o := options(filepath.Join(t.TempDir(), binName("stripe")), gh)

	err := Run(context.Background(), o, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("err = %v, want a target error", err)
	}
	if gh.hits.Load() != 0 {
		t.Errorf("GitHub was contacted %d times for a target that does not exist", gh.hits.Load())
	}
}
