//go:build darwin

package ledgerfile

import "golang.org/x/sys/unix"

// platformFSType reads the mount's filesystem type name. Unlike Linux,
// darwin's statfs carries the NAME ("nfs", "smbfs", "apfs"), which is
// exactly what networkFSTypes keys on, so no magic-number table is needed.
func platformFSType(dir string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return "", err
	}
	name := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return string(name), nil
}
