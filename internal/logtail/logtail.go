// Package logtail reads the end of quil's log files, for the TUI's log viewer
// and the browser's Logs page. Moved from internal/tui so both read the same
// bytes the same way.
package logtail

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxBytes caps how much of a log is read into memory.
const MaxBytes = 256 * 1024

// TruncatedMarker heads a tail that dropped older bytes.
const TruncatedMarker = "[... older lines truncated ...]\n"

// perFileFloor is the least share one MCP log gets in JoinDir.
const perFileFloor = 4 * 1024

// Tail reads up to maxBytes from the END of path, starting at a line boundary
// (or, when the window holds no newline, at a whole UTF-8 character).
//
// A link or any other non-regular file is refused: Lstat first, then a
// re-check through the open handle, so a swap between the two is refused too
// (the hardening of internal/persist/notes.go).
func Tail(path string, maxBytes int) (string, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !li.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q (mode=%v)", path, li.Mode())
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q after open", path)
	}
	size := stat.Size()
	if size <= int64(maxBytes) {
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if _, err := f.Seek(size-int64(maxBytes), io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i+1 < len(buf) {
		buf = buf[i+1:]
	} else {
		for k := 0; k < utf8.UTFMax-1 && len(buf) > 0 && !utf8.RuneStart(buf[0]); k++ {
			buf = buf[1:]
		}
	}
	return TruncatedMarker + string(buf), nil
}

// JoinDir joins the *.log files of dir, most recently modified first, each
// under a header line and capped at maxBytes/len(files) (at least 4 KiB), so
// one huge file cannot squeeze out the others. files is how many were found;
// err is set only when dir cannot be listed.
func JoinDir(dir string, maxBytes int) (text string, files int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, err
	}
	type logFile struct {
		name string
		info os.FileInfo
	}
	var list []logFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		list = append(list, logFile{name: e.Name(), info: info})
	}
	if len(list) == 0 {
		return "", 0, nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].info.ModTime().After(list[j].info.ModTime()) })
	perFile := max(maxBytes/len(list), perFileFloor)
	var b strings.Builder
	for _, f := range list {
		fmt.Fprintf(&b, "\n========== %s  (%s, %d bytes) ==========\n\n",
			f.name, f.info.ModTime().Format("2006-01-02 15:04:05"), f.info.Size())
		tail, terr := Tail(filepath.Join(dir, f.name), perFile)
		if terr != nil {
			fmt.Fprintf(&b, "(read error: %v)\n", terr)
			continue
		}
		b.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), len(list), nil
}
