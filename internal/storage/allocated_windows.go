package storage

import "io/fs"

// allocatedBytes is a file's length: Windows' FileInfo reports no allocation,
// and NTFS keeps small files inside the MFT record anyway.
func allocatedBytes(info fs.FileInfo) int64 { return info.Size() }
