//go:build unix

package ingest

import (
	"io/fs"
	"syscall"
)

// fileID returns the device and inode of a file, so a replaced file is read again
// from the start.
func fileID(fi fs.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}
