//go:build unix

package clientauth

import "os"

// syncDir fsyncs a directory, so a rename inside it survives a crash: the
// rename changes the directory, not the file, and the file's own Sync does
// not cover it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}
