package ledgerfile

import (
	"errors"
	"fmt"
)

// ErrNetworkFilesystem is a ledger home on a filesystem whose lock cannot be
// trusted. A sentinel so a caller can classify the refusal rather than match
// its message.
var ErrNetworkFilesystem = errors.New("ledger home is on a network filesystem")

// fsTypeFunc names the filesystem backing a directory. The empty string
// means "could not tell", which is ADMITTED: a platform this package has no
// statfs for, or a path whose statfs fails, must not become a platform where
// forge refuses to keep a ledger at all. The refusal is for filesystems we
// can POSITIVELY identify as network ones.
type fsTypeFunc func(dir string) (string, error)

// fsTypeOf is the seam tests replace. Production is the per-platform statfs
// in fstype_<goos>.go; a test injects a literal so "refuse NFS" is provable
// without an NFS mount, which no test environment here has.
var fsTypeOf fsTypeFunc = platformFSType

// networkFSTypes are the filesystems whose advisory locks do not serialize
// writers across machines. On every one of these, flock(2) may be silently
// local to one client, or emulated, or (for a stale mount) hang — so two
// machines sharing a $FORGE_LEDGER_HOME would both decide from the same
// history and both append, which is exactly the race this store exists to
// close.
//
// Refusing is the honest answer rather than a caveat in a doc comment: a
// ledger that looks locked and is not is worse than no ledger, because the
// corruption shows up as a lost promotion long after the command succeeded.
var networkFSTypes = map[string]string{
	"nfs":        "NFS",
	"nfs4":       "NFS",
	"smbfs":      "SMB/CIFS",
	"cifs":       "SMB/CIFS",
	"smb2":       "SMB/CIFS",
	"afpfs":      "AFP",
	"webdav":     "WebDAV",
	"fuse.sshfs": "sshfs",
	"9p":         "9P",
}

// checkLockableFS refuses a ledger home whose filesystem cannot hold a
// trustworthy lock.
func checkLockableFS(dir string) error {
	name, err := fsTypeOf(dir)
	if err != nil || name == "" {
		// Could not tell. Admit: see fsTypeFunc.
		return nil
	}
	pretty, bad := networkFSTypes[name]
	if !bad {
		return nil
	}
	return fmt.Errorf("%w: %s is %s, where an advisory lock does not serialize writers across machines.\n"+
		"  Two machines promoting at once would both decide from the same history and both append, losing one.\n"+
		"  Point $FORGE_LEDGER_HOME at a local disk, or keep the ledger on a control plane (which is how a TEAM shares one):\n"+
		"    declare forge.ControlPlane on the env, then: forge ledger import --from-file-ledger",
		ErrNetworkFilesystem, dir, pretty)
}
