package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	return fetchReleaseFrom(ctx, "", version, p)
}

// fetchReleaseFrom is FetchRelease with the GitHub API host injectable, so the
// whole download → verify → repack path can be tested against an httptest
// server instead of the real network. Empty baseURL means the real API.
//
// Pointing the Checker at a test host is sufficient to redirect everything: the
// asset download URLs come from the release JSON it returns, so the Stager
// follows them without needing its own seam.
func fetchReleaseFrom(ctx context.Context, baseURL, version string, p Platform) (Source, error) {
	rel, err := (&update.Checker{BaseURL: baseURL}).Release(ctx, version)
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
		path, info, err := findBinary(dir, name, p)
		if err != nil {
			return Source{}, err
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
		// Typeflag explicitly: the zero value is the deprecated TypeRegA. A
		// fixed ModTime keeps the archive byte-identical for identical inputs,
		// which matters because its sha256 is what the remote re-verifies.
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0).UTC(),
		}
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

// findBinary locates one binary inside a --from-dir directory.
//
// Two layouts are accepted, because the two obvious sources of binaries name
// them differently: a release archive unpacks to plain `quil`/`quild`, while
// this repo's own `scripts/dev.sh cross` writes `dist/quil-linux-arm64` and
// friends. A dev build has no release to fetch, so --from-dir IS its only
// route — requiring a manual rename first would make the documented workflow
// fail on its first use.
func findBinary(dir, name string, p Platform) (string, os.FileInfo, error) {
	candidates := []string{
		name,
		fmt.Sprintf("%s-%s-%s", name, p.GOOS, p.GOARCH),
	}
	for _, candidate := range candidates {
		path := filepath.Join(dir, candidate)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, info, nil
		}
	}
	return "", nil, fmt.Errorf("%s contains no %s for %s: looked for %s",
		dir, name, p, strings.Join(candidates, " and "))
}

// checkBinaryFormat rejects a binary that cannot run on the target.
//
// Both the executable format AND the CPU architecture are checked. Format alone
// would miss the more likely --from-dir mistake of the two: same-OS, wrong-arch
// binaries (amd64 build pushed to an arm64 server) are ELF either way, and
// pushing them replaces BOTH remote binaries with ones that cannot exec. The
// far-side symptom is exit 126 or 127 — and 127 is indistinguishable from "not
// installed", so it presents as a loop rather than a failure.
//
// Unrecognised architectures pass rather than fail: this is a guard against an
// obvious mistake, not an allowlist, and quil only publishes amd64 and arm64
// (PlatformFor rejects everything else long before this runs).
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
	if arch, ok := binaryArch(body); ok && arch != p.GOARCH {
		return fmt.Errorf("%s is built for %s, but the remote host is %s", name, arch, p)
	}
	return nil
}

// ELF and Mach-O architecture identifiers, from their respective ABI specs.
const (
	elfMachineAMD64   = 0x3e   // EM_X86_64
	elfMachineARM64   = 0xb7   // EM_AARCH64
	machoCPUTypeAMD64 = 0x01000007
	machoCPUTypeARM64 = 0x0100000c
)

// binaryArch reports the GOARCH an executable targets. ok is false when the
// architecture cannot be determined — a truncated header, a format not handled
// here, or a universal Mach-O binary, which carries several at once and so
// cannot be wrong about any single one.
func binaryArch(body []byte) (arch string, ok bool) {
	switch {
	case bytes.HasPrefix(body, []byte("\x7fELF")):
		// e_machine is a 2-byte field at offset 0x12, in the byte order named
		// by EI_DATA (e_ident[5]): 1 little-endian, 2 big-endian.
		if len(body) < 0x14 {
			return "", false
		}
		var machine uint16
		switch body[5] {
		case 1:
			machine = binary.LittleEndian.Uint16(body[0x12:0x14])
		case 2:
			machine = binary.BigEndian.Uint16(body[0x12:0x14])
		default:
			return "", false
		}
		switch machine {
		case elfMachineAMD64:
			return "amd64", true
		case elfMachineARM64:
			return "arm64", true
		}
	case bytes.HasPrefix(body, []byte{0xcf, 0xfa, 0xed, 0xfe}): // 64-bit little-endian
		if len(body) < 8 {
			return "", false
		}
		switch binary.LittleEndian.Uint32(body[4:8]) {
		case machoCPUTypeAMD64:
			return "amd64", true
		case machoCPUTypeARM64:
			return "arm64", true
		}
	case bytes.HasPrefix(body, []byte{0xfe, 0xed, 0xfa, 0xcf}): // 64-bit big-endian
		if len(body) < 8 {
			return "", false
		}
		switch binary.BigEndian.Uint32(body[4:8]) {
		case machoCPUTypeAMD64:
			return "amd64", true
		case machoCPUTypeARM64:
			return "arm64", true
		}
	}
	return "", false
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
