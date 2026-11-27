package toolfetch

import (
	"errors"
	"fmt"
)

// The Windows half of the trust check (the package doc's threat model): a
// cache on Windows is protected by its ACLs, not by mode bits, so forge reads
// the owner and DACL of every component and refuses the cache unless they
// prove it private. The decision is evaluateACL, a pure function over a
// plain model of the security descriptor, so it is table-tested on every
// OS; acl_windows.go is only the glue that reads the descriptor.

// Access-mask bits, as winnt.h spells them. Kept here rather than taken from
// golang.org/x/sys/windows so the evaluation compiles and is tested on every
// OS.
const (
	fileWriteData   = 0x00000002 // a file's data; a directory: add a file
	fileAppendData  = 0x00000004 // a file's data; a directory: add a subdirectory
	fileWriteEA     = 0x00000010
	fileDeleteChild = 0x00000040 // a directory: delete or rename any child
	fileWriteAttrs  = 0x00000100
	accessDelete    = 0x00010000
	readControl     = 0x00020000
	writeDAC        = 0x00040000
	writeOwner      = 0x00080000
	synchronize     = 0x00100000
	genericAll      = 0x10000000
	genericWrite    = 0x40000000

	// fileGenericWrite and fileAllAccess are the file object's generic
	// mapping of GENERIC_WRITE and GENERIC_ALL.
	fileGenericWrite = readControl | fileWriteData | fileWriteAttrs | fileWriteEA | fileAppendData | synchronize
	fileAllAccess    = 0x001F01FF
)

// ACE header values.
const (
	aceTypeAllowed  = 0
	aceTypeDenied   = 1
	aceInheritOnly  = 0x08
	sidCreatorOwner = "S-1-3-0"
	sidOwnerRights  = "S-1-3-4"
	sidSystem       = "S-1-5-18"
	sidAdmins       = "S-1-5-32-544"
	sidInstaller    = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" // NT SERVICE\TrustedInstaller
)

// What another principal must not be granted, by role.
const (
	// forbiddenOwned is for what forge creates (the release kind directory,
	// entries, their files): any way to write, add, delete or re-permission.
	forbiddenOwned = fileWriteData | fileAppendData | fileDeleteChild | accessDelete | writeDAC | writeOwner
	// forbiddenAncestor is for the directories above it. Adding a new child
	// (write/append data on a directory) is allowed, as a sticky /tmp is on
	// Unix: it cannot replace an existing child, which takes DELETE on the
	// child or FILE_DELETE_CHILD on its parent, both refused here. Without
	// this, every cache would be refused: the volume root's default ACL
	// grants Authenticated Users "create folders" on C:\ itself.
	forbiddenAncestor = fileDeleteChild | accessDelete | writeDAC | writeOwner
)

// aclRole says which rule a path is held to.
type aclRole int

const (
	aclOwned aclRole = iota
	aclAncestor
)

// winACE is one access-control entry of a DACL.
type winACE struct {
	Type  uint8
	Flags uint8
	Mask  uint32
	// SID is the trustee in string form ("S-1-1-0"); "" for an ACE type
	// whose layout forge does not read.
	SID string
}

// winSecurity is the part of a file's security descriptor the check reads.
type winSecurity struct {
	// Owner is the owner SID in string form.
	Owner string
	// NullDACL is true when the object has no DACL, or a NULL one: either
	// grants everyone full access.
	NullDACL bool
	ACEs     []winACE
}

// errACLUnreadable is a Windows host whose ACLs forge cannot read: it fails
// closed, as an untrusted cache.
var errACLUnreadable = errors.New("cannot read the ACL")

// evaluateACL decides whether path's security descriptor keeps it private to
// userSID (the current user), SYSTEM, BUILTIN\Administrators and
// TrustedInstaller. It errs on the side of refusing: deny ACEs are ignored
// (they can only narrow a grant, and ordering subtleties are not worth
// trusting), and an allow-type ACE forge cannot read refuses the path.
func evaluateACL(path string, sec winSecurity, userSID string, role aclRole) error {
	if userSID == "" {
		return fmt.Errorf("%s: %w: the current user's SID is unknown", path, errACLUnreadable)
	}
	trustedOwner := map[string]bool{userSID: true, sidSystem: true, sidAdmins: true, sidInstaller: true}
	if !trustedOwner[sec.Owner] {
		return fmt.Errorf("%s is owned by %s, not this user (%s), SYSTEM, Administrators or TrustedInstaller", path, sec.Owner, userSID)
	}
	if sec.NullDACL {
		return fmt.Errorf("%s has no DACL, which grants everyone full access", path)
	}
	trustedGrantee := map[string]bool{userSID: true, sidSystem: true, sidAdmins: true, sidInstaller: true, sidCreatorOwner: true, sidOwnerRights: true}
	forbidden := uint32(forbiddenOwned)
	if role == aclAncestor {
		forbidden = forbiddenAncestor
	}
	for _, ace := range sec.ACEs {
		if ace.Flags&aceInheritOnly != 0 {
			continue // applies to children only, and each child is checked itself
		}
		switch ace.Type {
		case aceTypeDenied:
			continue
		case aceTypeAllowed:
		default:
			return fmt.Errorf("%s has an access-control entry of type %d, which forge cannot evaluate", path, ace.Type)
		}
		if trustedGrantee[ace.SID] {
			continue
		}
		if granted := mapGeneric(ace.Mask) & forbidden; granted != 0 {
			return fmt.Errorf("%s grants %s write access (mask %#x)", path, ace.SID, granted)
		}
	}
	return nil
}

// mapGeneric expands the generic bits of a mask through the file object's
// generic mapping.
func mapGeneric(mask uint32) uint32 {
	if mask&genericAll != 0 {
		mask |= fileAllAccess
	}
	if mask&genericWrite != 0 {
		mask |= fileGenericWrite
	}
	return mask
}

// aclSource reads security descriptors on a Windows host. The zero value
// reads nothing, which makes every Windows-host check fail closed.
type aclSource struct {
	// read returns path's owner and DACL. nil on a host without ACLs.
	read func(path string) (winSecurity, error)
	// user is the current user's SID.
	user string
}

// checkACL holds path to role on a Windows host.
func (f *Fetcher) checkACL(path string, role aclRole) error {
	if f.acl.read == nil {
		return fmt.Errorf("%s: %w on this host, and forge refuses a Windows tool cache it cannot prove private", path, errACLUnreadable)
	}
	sec, err := f.acl.read(path)
	if err != nil {
		return fmt.Errorf("%s: %w: %v", path, errACLUnreadable, err)
	}
	return evaluateACL(path, sec, f.acl.user, role)
}
