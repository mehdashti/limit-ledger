//go:build !unix

package ingest

import "io/fs"

// fileID has no inode to offer here. A file that shrank is still detected by its
// size and read again from the start.
func fileID(fs.FileInfo) (dev, ino uint64) { return 0, 0 }
