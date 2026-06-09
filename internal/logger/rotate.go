package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RotatingWriter is an io.WriteCloser that writes to dir/base and, when the
// active file would exceed maxSize bytes, rotates it to a timestamped archive
// (stem-YYYYMMDD-HHMMSS.ext) and opens a fresh base file. At most maxFiles
// archives are kept; older ones are pruned by modification time. Safe for
// concurrent Write — the logger fans in from many goroutines.
type RotatingWriter struct {
	dir      string
	base     string // e.g. "quild.log"
	maxSize  int64
	maxFiles int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// NewRotatingWriter opens dir/base for appending. If the existing file already
// exceeds maxSizeBytes it is rotated immediately. maxSizeBytes <= 0 or
// maxFiles <= 0 are coerced to safe minimums so a misconfigured value can never
// disable writing entirely.
func NewRotatingWriter(dir, base string, maxSizeBytes int64, maxFiles int) (*RotatingWriter, error) {
	if maxSizeBytes <= 0 {
		maxSizeBytes = 5 << 20
	}
	if maxFiles <= 0 {
		maxFiles = 10
	}
	w := &RotatingWriter{dir: dir, base: base, maxSize: maxSizeBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	if w.size > w.maxSize {
		if err := w.rotate(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", w.dir, err)
	}
	f, err := os.OpenFile(filepath.Join(w.dir, w.base), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the active file, renames it to a timestamped archive, opens a
// fresh base file, and prunes old archives. Caller must hold w.mu.
func (w *RotatingWriter) rotate() error {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	ext := filepath.Ext(w.base)            // ".log"
	stem := w.base[:len(w.base)-len(ext)]  // "quild"
	ts := time.Now().Format("20060102-150405")
	dest := filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", stem, ts, ext))
	for i := 1; ; i++ { // collision suffix if two rotations land in the same second
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		if i > 100 { // cap: a non-IsNotExist Stat error (perm/I/O) must not spin forever — fall back to the last candidate name
			break
		}
		dest = filepath.Join(w.dir, fmt.Sprintf("%s-%s-%d%s", stem, ts, i, ext))
	}
	// Best-effort: on rename failure (cross-device, file locked on Windows) we keep writing to the original path rather than dropping log data.
	_ = os.Rename(filepath.Join(w.dir, w.base), dest)
	if err := w.open(); err != nil {
		return err
	}
	w.prune(stem, ext)
	return nil
}

// prune deletes all but the newest maxFiles archives (by modification time, so
// same-second collision suffixes can't fool a name sort).
func (w *RotatingWriter) prune(stem, ext string) {
	// Glob only errors on a malformed pattern; len(nil) <= maxFiles short-circuits safely.
	matches, _ := filepath.Glob(filepath.Join(w.dir, stem+"-*"+ext))
	if len(matches) <= w.maxFiles {
		return
	}
	type fi struct {
		path string
		mod  time.Time
	}
	infos := make([]fi, 0, len(matches))
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		infos = append(infos, fi{m, st.ModTime()})
	}
	if len(infos) <= w.maxFiles { // re-check on infos: failed Stats may have dropped entries, so the delete-slice bound must be infos-based to avoid a negative index
		return
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].mod.Before(infos[j].mod) })
	for _, old := range infos[:len(infos)-w.maxFiles] {
		// Best-effort: a failed prune just leaves an old archive on disk; non-fatal.
		_ = os.Remove(old.path)
	}
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
