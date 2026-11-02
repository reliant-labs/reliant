// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The file viewer's credential guard.
//
// The viewer rpcs (GetFileContent, GetFileMetadata, GetFilePreviewInfo,
// GetFilePreview) open an absolute path outside the workspace on purpose: a
// sibling repo, the module cache, ~/.config, whatever an agent mentioned. So
// they cannot be confined to the workspace. What they must not open is a
// credential store: anything that can drive the web UI as the user (an XSS, a
// link that opens the viewer on ~/.ssh/id_ed25519) would otherwise read the
// user's keys through it (the 2026-11-01 review, RF-3). The server marks a
// viewer read with the request's workspace root (ViewerRoot), and the daemon,
// which alone can resolve symlinks on that disk, refuses it here.

// ErrViewerCredential is the sentinel for a viewer read refused because the
// path is a credential store. The server maps its text to PermissionDenied.
var ErrViewerCredential = errors.New("refused: the file viewer does not open credential stores")

// viewerNarrowed replaces a protectedHome directory with the credential files
// inside it, for the viewer only: ~/.claude and ~/.codex also hold settings,
// skills and plans an agent links to, and only these files are logins.
var viewerNarrowed = map[string][]string{
	".claude": {".claude/.credentials.json"},
	".codex":  {".codex/auth.json"},
}

// viewerOnlyHome are credential stores the viewer refuses beyond
// protectedHome: browser profiles (cookies, saved passwords) and the
// keychains. The shell sandbox does not hide these, so they are not in
// protectedHome.
var viewerOnlyHome = []homeEntry{
	{".mozilla", true},
	{".config/google-chrome", true},
	{".config/chromium", true},
	{".config/BraveSoftware", true},
	{".config/microsoft-edge", true},
	{".local/share/keyrings", true},
	{".password-store", true},
	{"Library/Application Support/Google/Chrome", true},
	{"Library/Application Support/Chromium", true},
	{"Library/Application Support/BraveSoftware", true},
	{"Library/Application Support/Microsoft Edge", true},
	{"Library/Application Support/Firefox", true},
	{"Library/Safari", true},
	{"Library/Cookies", true},
	{"Library/Keychains", true},
	{"AppData/Local/Google/Chrome/User Data", true},
	{"AppData/Local/Microsoft/Edge/User Data", true},
	{"AppData/Roaming/Mozilla/Firefox", true},
}

// viewerProtected returns the credential stores the viewer refuses for a user
// whose home is home.
func viewerProtected(home, forgeCredentials string) []Credential {
	var out []Credential
	if home != "" {
		for _, e := range protectedHome {
			if files, ok := viewerNarrowed[e.rel]; ok {
				for _, f := range files {
					out = appendCredential(out, Credential{Path: filepath.Join(home, f)})
				}
				continue
			}
			out = appendCredential(out, Credential{Path: filepath.Join(home, e.rel), Dir: e.dir})
		}
		for _, e := range viewerOnlyHome {
			out = appendCredential(out, Credential{Path: filepath.Join(home, e.rel), Dir: e.dir})
		}
	}
	for _, p := range reliantCredentialFiles(home, forgeCredentials) {
		out = appendCredential(out, Credential{Path: p})
	}
	return out
}

// DenyViewerRead returns an error wrapping ErrViewerCredential when the file
// viewer must not open path, and nil otherwise. root is the workspace the
// request was made in. It judges a pathname, so it is all a stat needs; a
// read of the file's bytes also checks the file it opened (CheckViewerFile).
//
// A path is refused when it, or what it resolves to through symlinks, lies in
// a credential store, so a link inside the project to ~/.ssh/id_rsa is
// refused like the path itself, and when it is a hard link to a file in one.
// A file of the workspace is still served when it is reached through a store
// that lives in it by link (a dotfiles repo that ~/.ssh points into); a
// workspace that contains the store itself (one rooted at the home directory)
// does not exempt it, and neither does a hard link.
func DenyViewerRead(path, root string) error {
	home, _ := os.UserHomeDir()
	return denyViewerRead(path, root, viewerProtected(home, forgeCredentialsPath()))
}

// CheckViewerFile is DenyViewerRead for a file the viewer has already opened
// by path: it refuses f unless it is not a credential store AND it is the file
// path names now. The caller then reads f, never the pathname again.
// Checking a pathname and then reopening it is a race: a project symlink
// pointing at README.md during the check can point at ~/.ssh/id_rsa by the
// open. Opening first and requiring the resolved path to name the open file
// closes it; a path that changed in between is refused rather than retried.
// Call DenyViewerRead before the open too, so that a missing file in a store
// is a refusal, not a "not found" that tells the caller what ~/.ssh holds.
func CheckViewerFile(f *os.File, path, root string) error {
	home, _ := os.UserHomeDir()
	return checkViewerFile(f, path, root, viewerProtected(home, forgeCredentialsPath()))
}

func checkViewerFile(f *os.File, path, root string, protected []Credential) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	abs, real := viewerPaths(path)
	if ri, err := os.Stat(real); err != nil || !os.SameFile(info, ri) {
		return fmt.Errorf("%w (%s changed while it was opened)", ErrViewerCredential, path)
	}
	return checkViewerRead(abs, real, root, info, protected)
}

func denyViewerRead(path, root string, protected []Credential) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, real := viewerPaths(path)
	info, err := os.Stat(real)
	if err != nil {
		info = nil
	}
	return checkViewerRead(abs, real, root, info, protected)
}

// viewerPaths returns path made absolute, and that with its symlinks resolved.
func viewerPaths(path string) (abs, real string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	return abs, ResolveLoose(abs)
}

// checkViewerRead is the guard itself. info is the file being read (nil when
// it does not exist); it is what a hard link is recognised by.
func checkViewerRead(abs, real, root string, info os.FileInfo, protected []Credential) error {
	realRoot := ""
	if strings.TrimSpace(root) != "" {
		realRoot = ResolveLoose(root)
	}
	for _, c := range protected {
		realStore := ResolveLoose(c.Path)
		hit := c.covers(abs) || c.covers(real) ||
			Credential{Path: realStore, Dir: c.Dir}.covers(real)
		if !hit && info != nil && sameAsStored(info, realStore, c.Dir) {
			// A hard link, or a second mount of the same disk. Where it sits
			// says nothing about what it is, so no exemption below applies.
			return fmt.Errorf("%w (%s)", ErrViewerCredential, c.Path)
		}
		if !hit {
			continue
		}
		// Where the store sits, its own name unresolved: ~/.ssh stays ~/.ssh
		// when it links into a dotfiles repo, and becomes /data/u/.ssh when
		// the home directory itself is a link to /data/u.
		storeAt := filepath.Join(ResolveLoose(filepath.Dir(c.Path)), filepath.Base(c.Path))
		if realRoot != "" && within(real, realRoot) && !within(storeAt, realRoot) {
			continue
		}
		return fmt.Errorf("%w (%s)", ErrViewerCredential, c.Path)
	}
	return nil
}

// sameAsStored reports whether info is the credential file store or, for a
// directory store, a file inside it. A directory is only walked for a file
// that may have a second name (more than one link), which an ordinary project
// file does not.
func sameAsStored(info os.FileInfo, store string, dir bool) bool {
	if !dir {
		si, err := os.Stat(store)
		return err == nil && os.SameFile(info, si)
	}
	if !info.Mode().IsRegular() || !mayHaveOtherNames(info) {
		return false
	}
	found := false
	_ = filepath.WalkDir(store, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if si, err := d.Info(); err == nil && os.SameFile(info, si) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// covers reports whether p is the credential location c or, for a directory,
// lies beneath it.
func (c Credential) covers(p string) bool {
	if p == c.Path {
		return true
	}
	return c.Dir && within(p, c.Path)
}
