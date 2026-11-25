//go:build windows

package toolfetch

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformACL reads security descriptors with GetNamedSecurityInfo, for the
// current process's user. A user SID that cannot be read leaves user "",
// which evaluateACL refuses: fail closed.
func platformACL() aclSource {
	src := aclSource{read: readWindowsSecurity}
	if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		src.user = tu.User.Sid.String()
	}
	return src
}

// readWindowsSecurity returns path's owner and DACL as a winSecurity.
func readWindowsSecurity(path string) (winSecurity, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return winSecurity{}, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return winSecurity{}, err
	}
	sec := winSecurity{}
	if owner != nil {
		sec.Owner = owner.String()
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		sec.NullDACL = true
		return sec, nil
	}
	if err != nil {
		return winSecurity{}, err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return winSecurity{}, err
		}
		e := winACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask)}
		// ACCESS_ALLOWED_ACE and ACCESS_DENIED_ACE share this layout, the
		// SID starting at SidStart; other types are refused by evaluateACL
		// without reading their SID.
		if e.Type == aceTypeAllowed || e.Type == aceTypeDenied {
			e.SID = (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() // #nosec G103 -- the documented ACE layout
		}
		sec.ACEs = append(sec.ACEs, e)
	}
	return sec, nil
}
