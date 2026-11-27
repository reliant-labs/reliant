//go:build !unix

package toolfetch

import "io/fs"

// fileOwner reports no owner: on Windows the cache's access control is its
// ACL, which forge does not inspect (see the package doc's threat model).
func fileOwner(fs.FileInfo) (uid, gid uint32, ok bool) { return 0, 0, false }
