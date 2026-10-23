//go:build windows

package openfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no machine-wide open-file listing, but the Restart Manager
// (rstrtmgr.dll) answers "which processes hold these files". So a Windows
// Snapshot is lazy: Holds enumerates the files under the queried path and
// asks the Restart Manager about them, all under the Timeout fixed at Take.
//
// Every failure — enumeration, a Restart Manager error, an expired deadline —
// answers "held", the same fail-closed stance as a failed lsof.
//
// Directories are checked separately, because the Restart Manager tracks
// files only and would miss a process whose WORKING DIRECTORY is under the
// path — a shell or dev server sitting in a worktree with no file open. That
// process is exactly the one a reclaimer must not empty a tree around:
// os.RemoveAll deletes every file first and only fails on the directory
// itself. dirHeld opens each directory for DELETE while sharing everything;
// that fails with ERROR_SHARING_VIOLATION only when some open handle denies
// delete-sharing, which is what a working-directory handle does, and is
// precisely the condition under which the directory could not be removed.
// A handle that permits delete (Explorer's change-notification watch) does
// not count, correctly: it does not stop the removal either.

const (
	errorMoreData = 234 // ERROR_MORE_DATA: RmGetList has processes to report.

	// cchRmSessionKey is CCH_RM_SESSION_KEY (32); the key buffer needs one
	// more for the terminating NUL.
	cchRmSessionKey = 32

	// filesPerSession bounds one RmRegisterResources call. Microsoft documents
	// no per-session cap (only that each call is expensive, so files should be
	// grouped, and that the system allows 64 concurrent sessions), so this is a
	// conservative batch size; each batch gets a fresh session.
	filesPerSession = 256
)

var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

func takeLazy(ctx context.Context) (Snapshot, error) {
	if err := rstrtmgr.Load(); err != nil {
		return Snapshot{}, fmt.Errorf("restart manager unavailable: %w", err)
	}
	// Each Holds gets its own deadline: a reclaimer probing many candidates
	// would otherwise see every probe after the first Timeout answer "held".
	return Snapshot{probe: func(path string) bool {
		return heldUnder(ctx, time.Now().Add(Timeout), path)
	}}, nil
}

// heldUnder reports whether any process holds a file at or under path.
func heldUnder(ctx context.Context, deadline time.Time, path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else if !errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var batch []string
	flush := func() (bool, error) {
		if len(batch) == 0 {
			return false, nil
		}
		held, err := anyHeld(batch)
		batch = batch[:0]
		return held, err
	}
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished: nothing left to hold
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		if d.IsDir() {
			if dirHeld(p) {
				return errHeld
			}
			return nil
		}
		batch = append(batch, p)
		if len(batch) < filesPerSession {
			return nil
		}
		held, err := flush()
		if err != nil {
			return err
		}
		if held {
			return errHeld
		}
		return nil
	})
	switch {
	case errors.Is(walkErr, errHeld):
		return true
	case walkErr != nil:
		return true
	}
	held, err := flush()
	return err != nil || held
}

var errHeld = errors.New("held")

// dirHeld reports whether some process holds dir in a way that would stop
// its removal — in practice, as its working directory. See the file comment.
// Any failure other than "gone" answers held (fail closed).
func dirHeld(dir string) bool {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return true
	}
	h, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		// BACKUP_SEMANTICS is required to open a directory; OPEN_REPARSE_POINT
		// keeps a junction from being followed to its target.
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
	}
	_ = windows.CloseHandle(h)
	return false
}

// anyHeld asks the Restart Manager whether any process uses any of files.
func anyHeld(files []string) (bool, error) {
	var session uint32
	var key [cchRmSessionKey + 1]uint16
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return false, fmt.Errorf("RmStartSession: %w", windows.Errno(r))
	}
	defer func() { _, _, _ = procRmEndSession.Call(uintptr(session)) }()

	names := make([]*uint16, len(files))
	for i, f := range files {
		p, err := windows.UTF16PtrFromString(f)
		if err != nil {
			return false, err
		}
		names[i] = p
	}
	if r, _, _ := procRmRegisterResources.Call(uintptr(session), uintptr(len(names)), uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); r != 0 {
		return false, fmt.Errorf("RmRegisterResources: %w", windows.Errno(r))
	}
	// A zero-length buffer is enough: RmGetList reports ERROR_MORE_DATA with
	// the needed count when any process is using a registered file, and
	// success with a count of zero when none is.
	var needed, have, reasons uint32
	r, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&have)), 0, uintptr(unsafe.Pointer(&reasons)))
	switch r {
	case 0:
		return needed > 0, nil
	case errorMoreData:
		return true, nil
	}
	return false, fmt.Errorf("RmGetList: %w", windows.Errno(r))
}
