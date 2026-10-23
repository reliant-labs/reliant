//go:build linux

package ledgerfile

import "golang.org/x/sys/unix"

// linuxFSMagic maps the statfs magic numbers this package cares about to the
// names networkFSTypes keys on. Linux's statfs reports a magic number rather
// than a name, so the mapping is explicit; anything not listed returns "",
// which checkLockableFS admits.
var linuxFSMagic = map[int64]string{
	unix.NFS_SUPER_MAGIC:  "nfs",
	unix.SMB_SUPER_MAGIC:  "smbfs",
	unix.SMB2_SUPER_MAGIC: "smb2",
	unix.CIFS_SUPER_MAGIC: "cifs",
	unix.V9FS_MAGIC:       "9p",
}

func platformFSType(dir string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return "", err
	}
	return linuxFSMagic[int64(st.Type)], nil
}
