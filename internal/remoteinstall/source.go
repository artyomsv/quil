package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/update"
)

// binaryNames are the two executables a quil release archive carries, and the
// two this package installs.
var binaryNames = []string{"quil", "quild"}

// maxBinarySize bounds what --from-dir will read into memory. The real
// binaries are ~15 MB; this is loose enough not to bite and tight enough that
// pointing --from-dir at the wrong directory fails fast instead of exhausting
// memory.
const maxBinarySize = 200 << 20

// Source is a verified set of binaries ready to push, packed in the tar.gz
// shape the remote install script expects.
type Source struct {
	// Version is what will be installed, without a leading "v". Empty for a
	// --from-dir source, whose contents carry no version metadata.
	Version string

	// Archive is the tar.gz itself.
	Archive []byte

	// SHA256 is the archive's hex digest, computed here and re-checked on the
	// far side so a truncated transfer cannot install.
	SHA256 string
}

// FetchRelease downloads a release for the remote's platform and repacks it.
//
// version selects the release ("" means latest); it should normally be this
// TUI's own version, since installing a different one on the far side just
// inverts the mismatch it was called to fix.
//
// The download, checksum verification and archive extraction are delegated to
// update.Stager rather than reimplemented: it already takes the target platform
// as a parameter, verifies before extracting, and its extraction is hardened
// against hostile archive entry names. Repacking its output costs a temp dir
// and buys no second copy of that logic.
func FetchRelease(ctx context.Context, version string, p Platform) (Source, error) {
	rel, err := (&update.Checker{}).Release(ctx, version)
	if err != nil {
		return Source{}, fmt.Errorf("look up release: %w", err)
	}

	tmp, err := os.MkdirTemp("", "quil-remote-*")
	if err != nil {
		return Source{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	stager := &update.Stager{Root: tmp, GOOS: p.GOOS, GOARCH: p.GOARCH}
	if err := stager.Stage(ctx, rel); err != nil {
		return Source{}, fmt.Errorf("fetch %s for %s: %w", rel.TagName, p, err)
	}

	src, err := PackDir(filepath.Join(tmp, "staged", rel.Version()), p)
	if err != nil {
		return Source{}, err
	}
	src.Version = rel.Version()
	return src, nil
}

// PackDir builds a release-shaped archive from a directory holding quil and
// quild built for the remote's platform.
//
// This is the --from-dir path, and the only one available to a dev build, which
// has no matching release to fetch.
func PackDir(dir string, p Platform) (Source, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, name := range binaryNames {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return Source{}, fmt.Errorf("%s must contain %q built for %s: %w", dir, name, p, err)
		}
		if info.Size() > maxBinarySize {
			return Source{}, fmt.Errorf("%s is %d bytes, larger than the %d-byte limit", path, info.Size(), maxBinarySize)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return Source{}, fmt.Errorf("read %s: %w", path, err)
		}
		if err := checkBinaryFormat(name, body, p); err != nil {
			return Source{}, err
		}
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			return Source{}, fmt.Errorf("write tar header for %s: %w", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			return Source{}, fmt.Errorf("write %s into archive: %w", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return Source{}, fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Source{}, fmt.Errorf("close gzip: %w", err)
	}

	sum := sha256.Sum256(buf.Bytes())
	return Source{Archive: buf.Bytes(), SHA256: hex.EncodeToString(sum[:])}, nil
}

// checkBinaryFormat rejects a binary that obviously cannot run on the target.
//
// Only the executable-format magic is checked, not the architecture: the point
// is to catch the mistake --from-dir invites — pointing it at a local build
// directory holding this machine's binaries — before pushing something whose
// only symptom on the far side is exit 126 or 127, the latter being
// indistinguishable from "not installed".
func checkBinaryFormat(name string, body []byte, p Platform) error {
	if len(body) < 4 {
		return fmt.Errorf("%s is too small to be an executable", name)
	}
	switch {
	case bytes.HasPrefix(body, []byte("MZ")):
		return fmt.Errorf("%s is a Windows executable, but the remote host is %s", name, p)
	case p.GOOS == "linux" && !bytes.HasPrefix(body, []byte("\x7fELF")):
		return fmt.Errorf("%s is not an ELF executable, but the remote host is %s", name, p)
	case p.GOOS == "darwin" && !isMachO(body):
		return fmt.Errorf("%s is not a Mach-O executable, but the remote host is %s", name, p)
	}
	return nil
}

// isMachO reports whether body starts with a Mach-O magic number: 64-bit
// little- and big-endian, plus the universal ("fat") wrappers Apple uses for
// multi-architecture binaries.
func isMachO(body []byte) bool {
	for _, magic := range [][]byte{
		{0xcf, 0xfa, 0xed, 0xfe}, // 64-bit little-endian
		{0xfe, 0xed, 0xfa, 0xcf}, // 64-bit big-endian
		{0xca, 0xfe, 0xba, 0xbe}, // universal
		{0xbe, 0xba, 0xfe, 0xca}, // universal, byte-swapped
	} {
		if bytes.HasPrefix(body, magic) {
			return true
		}
	}
	return false
}
