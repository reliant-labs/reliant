//go:build windows

package toolfetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// grant adds an allow ACE for sid with mask to path's DACL, inherited by
// what is created beneath it.
func grant(t *testing.T, path string, sid *windows.SID, mask windows.ACCESS_MASK) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func everyone(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// The real reader on a real Windows ACL: a temp dir (it inherits the
// profile's per-user ACL) is trusted; one that grants Everyone write is
// refused before anything is downloaded.
func TestWindowsACLRefusesAnEveryoneWritableCache(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)

	private := rel.fetcher(t, "linux")
	if private.acl.user == "" {
		t.Fatal("platformACL found no user SID")
	}
	if _, err := private.Ensure(context.Background(), tool); err != nil {
		t.Fatalf("a cache under the per-user temp dir was refused: %v", err)
	}

	shared := t.TempDir()
	grant(t, shared, everyone(t), windows.FILE_GENERIC_WRITE)
	f := rel.fetcher(t, "linux")
	f.CacheDir = filepath.Join(shared, "release")
	before := rel.hits.Load()
	_, err := f.Ensure(context.Background(), tool)
	if !errors.Is(err, ErrUntrustedCache) || rel.hits.Load() != before {
		t.Fatalf("err = %v, want ErrUntrustedCache before any download", err)
	}
}

// An ancestor that lets Everyone delete or rename its children is refused.
func TestWindowsACLRefusesAnAncestorOthersCanEmpty(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	child := filepath.Join(parent, "cache")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Granted on the parent only, after the child exists, so the child's
	// own ACL stays private: only the ancestor rule can refuse this.
	sd, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.ACCESS_MASK(fileDeleteChild),
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP, TrusteeValue: windows.TrusteeValueFromSID(everyone(t))},
	}}, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	f := New()
	if _, err := f.trustedKindDir(child); !errors.Is(err, ErrUntrustedCache) {
		t.Fatalf("err = %v, want ErrUntrustedCache for an ancestor Everyone can empty", err)
	}
}

// The default location, %LocalAppData% (os.UserCacheDir), and every
// directory above it pass as they come: the inherited per-user profile ACL
// is private.
func TestWindowsACLTrustsTheDefaultCacheLocation(t *testing.T) {
	t.Parallel()
	dir, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no user cache dir: %v", err)
	}
	if _, err := New().trustedKindDir(dir); err != nil {
		t.Fatalf("the default cache location %s is refused: %v", dir, err)
	}
}
