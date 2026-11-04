package generator

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unicode/utf16"
)

// errPrivilegeNotHeld is Windows ERROR_PRIVILEGE_NOT_HELD: creating a
// symlink needs SeCreateSymbolicLinkPrivilege (Developer Mode or admin).
const errPrivilegeNotHeld = syscall.Errno(1314)

// symlinkOrJunction creates a relative directory symlink and, only when the
// OS refuses for lack of privilege, falls back to a junction — which needs
// none but must name an absolute target.
func symlinkOrJunction(rel, abs, link string, symlink func(oldname, newname string) error, junction func(target, link string) error) error {
	err := symlink(rel, link)
	if err == nil || !errors.Is(err, errPrivilegeNotHeld) {
		return err
	}
	return junction(abs, link)
}

const (
	ioReparseTagMountPoint = 0xA0000003
	mountPointHeaderSize   = 8 // tag, data length, reserved
	mountPointNameFields   = 8 // four uint16 offset/length fields
)

// junctionTarget validates target for a junction and returns it in the
// drive-letter form a mount-point reparse buffer needs (`C:\a\b`).
//
// A junction can only name a LOCAL volume: a UNC path (`\\server\share`)
// would encode as the malformed `\??\\\server\share` and fail obscurely at
// DeviceIoControl, so it is refused here with a reason. A `\\?\C:\...`
// long-path prefix is stripped; the substitute name adds its own `\??\`.
func junctionTarget(target string) (string, error) {
	t := strings.TrimPrefix(target, `\\?\`)
	if strings.HasPrefix(t, `UNC\`) || strings.HasPrefix(t, `\\`) {
		return "", fmt.Errorf("cannot junction to %s: a junction can only target a local drive, not a network share (enable Developer Mode so forge can use a symlink instead)", target)
	}
	if len(t) < 3 || t[1] != ':' || (t[2] != '\\' && t[2] != '/') ||
		!(t[0] >= 'A' && t[0] <= 'Z' || t[0] >= 'a' && t[0] <= 'z') {
		return "", fmt.Errorf("cannot junction to %s: want an absolute drive path like C:\\dir", target)
	}
	return t, nil
}

// mountPointReparseBuffer encodes a REPARSE_DATA_BUFFER for a junction to
// the absolute Win32 path target (e.g. `C:\a\b`); see junctionTarget.
func mountPointReparseBuffer(target string) []byte {
	substitute := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))

	pathBytes := (len(substitute) + 1 + len(print) + 1) * 2
	buf := make([]byte, mountPointHeaderSize+mountPointNameFields+pathBytes)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], ioReparseTagMountPoint)
	le.PutUint16(buf[4:], uint16(mountPointNameFields+pathBytes))
	// buf[6:8] reserved
	le.PutUint16(buf[8:], 0)                              // SubstituteNameOffset
	le.PutUint16(buf[10:], uint16(len(substitute)*2))     // SubstituteNameLength (no NUL)
	le.PutUint16(buf[12:], uint16((len(substitute)+1)*2)) // PrintNameOffset
	le.PutUint16(buf[14:], uint16(len(print)*2))          // PrintNameLength (no NUL)
	off := mountPointHeaderSize + mountPointNameFields
	for _, u := range substitute {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	off += 2 // NUL
	for _, u := range print {
		le.PutUint16(buf[off:], u)
		off += 2
	}
	return buf
}
