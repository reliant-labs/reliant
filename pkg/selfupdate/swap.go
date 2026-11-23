package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// swapFS is the file operations that put a binary at an install path: the
// host's ([hostFS]), or a test's stand-in that models Windows' lock on a
// running image, so the Windows path is tested on every platform.
type swapFS struct {
	rename func(oldpath, newpath string) error
	remove func(path string) error
	// moveAside: the file at an install path may be a running image, which
	// Windows lets a process rename but neither replace nor remove. So it is
	// renamed to <stem>.old-<unixnano><ext> in the same directory first, and
	// removed as soon as nothing runs it ([sweepAside] retries at the next
	// start). Unix renames over a running binary, so it never moves one aside.
	moveAside bool
}

var hostFS = swapFS{rename: hostRename, remove: os.Remove, moveAside: runningImageLocked}

// orHost is f, or the host's operations when f is the zero value.
func (f swapFS) orHost() swapFS {
	if f.rename == nil || f.remove == nil {
		return hostFS
	}
	return f
}

// moveJournal names the files of one move-aside replacement, written durably
// before the install path is first renamed and removed once it holds a
// binary again. Between the two renames the install path is missing; if the
// process dies there, the journal is how the next [Boot] that can run
// (the image set aside, started by hand) puts its own install back:
// [recoverMove].
type moveJournal struct {
	// Path is the install path; Aside the name its old file was renamed
	// to; Staged the copy that was to take its place.
	Path   string `json:"path"`
	Aside  string `json:"aside"`
	Staged string `json:"staged"`
}

// journalPath is the move journal of the state directory dir.
func journalPath(dir string) string { return filepath.Join(dir, "move.json") }

// replaceWithCopy puts a copy of src at dst by copy-to-sibling then rename:
// atomic, and a running dst keeps its old inode. Where a running image
// cannot be replaced (moveAside), dst is first renamed aside, named from now:
// the move is recorded in the journal file first (the caller holds the state
// lock), and if the copy then cannot take dst's name, dst is put back. The
// journal is unused where nothing is moved aside.
func (f swapFS) replaceWithCopy(src, dst string, now time.Time, journal string) error {
	dir := filepath.Dir(dst)
	tmp, err := copyToTemp(src, dir, "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return fmt.Errorf("copy %s beside %s: %w", src, dst, err)
	}
	if !f.moveAside {
		if err := f.rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename over %s: %w", dst, err)
		}
		return syncDir(dir)
	}
	aside, err := f.setAside(dst, now, func(aside string) error {
		data, err := json.Marshal(moveJournal{Path: dst, Aside: aside, Staged: tmp})
		if err != nil {
			return err
		}
		return writeFileAtomic(journal, data, 0o600)
	})
	if err != nil {
		_ = os.Remove(tmp)
		_ = os.Remove(journal)
		return fmt.Errorf("move %s aside: %w", dst, err)
	}
	if err := f.rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		err = fmt.Errorf("rename over %s: %w", dst, err)
		if aside != "" {
			// Never leave the install path empty.
			if restoreErr := f.rename(aside, dst); restoreErr != nil {
				// The journal stays: the next Boot that can run retries.
				return errors.Join(err, restoreErr)
			}
		}
		_ = os.Remove(journal)
		return err
	}
	_ = os.Remove(journal)
	if aside != "" {
		// Fails while aside is a running image; the next start sweeps it.
		_ = f.remove(aside)
	}
	return syncDir(dir)
}

// setAside renames path to a free <stem>.old-<unixnano><ext> beside it and
// returns that name, or "" when path does not exist. It never replaces a file:
// an earlier one may be another instance's running image. record is called
// with the chosen name before the rename, and an error from it aborts.
func (f swapFS) setAside(path string, now time.Time, record func(aside string) error) (string, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	for i := int64(0); ; i++ {
		aside := asideName(path, now.UnixNano()+i)
		if _, err := os.Lstat(aside); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if record != nil {
			if err := record(aside); err != nil {
				return "", fmt.Errorf("record the move: %w", err)
			}
		}
		if err := f.rename(path, aside); err != nil {
			return "", err
		}
		return aside, nil
	}
}

// recoverMove finishes a move-aside replacement that a dead process left
// half done, from the journal in the state directory dir; the caller holds
// the state lock, so no live process is mid-move. When the install path is
// missing, the image set aside is renamed back: the binary that was there
// before the interrupted move, so state.json, written only after a move
// completes, still describes it. It returns the install path it restored
// ("" when there was nothing to restore).
func recoverMove(f swapFS, dir, exe string) (string, error) {
	journal := journalPath(dir)
	data, err := os.ReadFile(journal)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("selfupdate: read the move journal: %w", err)
	}
	var j moveJournal
	if err := json.Unmarshal(data, &j); err != nil || j.Path == "" {
		// Torn before the move began: the journal is written before the
		// first rename, so nothing was moved under it.
		return "", os.Remove(journal)
	}
	if err := j.validate(exe); err != nil {
		return "", fmt.Errorf("selfupdate: invalid move journal: %w", err)
	}
	restored := ""
	if _, err := os.Lstat(j.Path); errors.Is(err, fs.ErrNotExist) {
		if j.Aside == "" {
			return "", fmt.Errorf("selfupdate: %s is missing and the move journal names no image to restore", j.Path)
		}
		if err := f.rename(j.Aside, j.Path); err != nil {
			return "", fmt.Errorf("selfupdate: restore %s from %s: %w", j.Path, j.Aside, err)
		}
		restored = j.Path
	} else if err != nil {
		return "", fmt.Errorf("selfupdate: %s: %w", j.Path, err)
	}
	// The install path holds a binary: the move completed, or was undone.
	if j.Staged != "" && j.Staged != j.Path {
		_ = os.Remove(j.Staged)
	}
	if err := os.Remove(journal); err != nil {
		return restored, err
	}
	return restored, syncDir(filepath.Dir(j.Path))
}

// validate binds recovery to the running install (or its renamed image),
// never to an arbitrary binary named by writable state. All journal operands
// must be clean absolute siblings with the names our swap generates. Reject
// symlinks and directories as well, before performing any mutation.
func (j moveJournal) validate(exe string) error {
	if !filepath.IsAbs(exe) || !filepath.IsAbs(j.Path) || filepath.Clean(j.Path) != j.Path ||
		j.Path != originalInstall(exe) || originalInstall(j.Path) != j.Path {
		return errors.New("install path does not match the running binary")
	}
	if j.Aside == j.Path || filepath.Dir(j.Aside) != filepath.Dir(j.Path) || filepath.Clean(j.Aside) != j.Aside ||
		!isAsideOf(filepath.Base(j.Path), filepath.Base(j.Aside)) {
		return errors.New("unexpected aside path")
	}
	prefix := "." + filepath.Base(j.Path) + ".new-"
	suffix, ok := strings.CutPrefix(filepath.Base(j.Staged), prefix)
	if filepath.Dir(j.Staged) != filepath.Dir(j.Path) || filepath.Clean(j.Staged) != j.Staged || !ok || suffix == "" {
		return errors.New("unexpected staged path")
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return errors.New("unexpected staged suffix")
		}
	}
	for _, path := range []string{j.Path, j.Aside, j.Staged} {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
	}
	return nil
}

// asideName is <dir>/<stem>.old-<ns><ext> for path <dir>/<stem><ext>:
// reliant.exe becomes reliant.old-<ns>.exe, still an .exe.
func asideName(path string, ns int64) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.old-%d%s", strings.TrimSuffix(base, ext), ns, ext))
}

// originalInstall removes exactly one generated aside suffix, if present.
// Extensionless installs need special care: filepath.Ext treats .old-<ns>
// itself as their extension.
func originalInstall(exe string) string {
	base := filepath.Base(exe)
	ext := filepath.Ext(base)
	if strings.HasPrefix(ext, ".old-") {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	if i := strings.LastIndex(stem, ".old-"); i > 0 {
		install := stem[:i] + ext
		if isAsideOf(install, base) {
			return filepath.Join(filepath.Dir(exe), install)
		}
	}
	return exe
}

// isAsideOf reports whether name is an [asideName] of the binary named base.
func isAsideOf(base, name string) bool {
	ext := filepath.Ext(base)
	rest, ok := strings.CutPrefix(name, strings.TrimSuffix(base, ext)+".old-")
	if !ok || !strings.HasSuffix(rest, ext) {
		return false
	}
	digits := strings.TrimSuffix(rest, ext)
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sweepAside removes the images earlier swaps set aside beside exe. One that
// a process still runs cannot be removed and stays for a later start. The
// caller holds the state lock, so no move is in flight.
func sweepAside(f swapFS, exe string) {
	if !f.moveAside || exe == "" {
		return
	}
	dir, base := filepath.Dir(exe), filepath.Base(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() && isAsideOf(base, e.Name()) {
			_ = f.remove(filepath.Join(dir, e.Name()))
		}
	}
}
