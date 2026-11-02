// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"errors"
	"fmt"
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
// request was made in.
//
// A path is refused when it, or what it resolves to through symlinks, lies in
// a credential store, so a link inside the project to ~/.ssh/id_rsa is
// refused like the path itself. A file of the workspace is still served when
// it is reached through a store that lives in it by link (a dotfiles repo that
// ~/.ssh points into); a workspace that contains the store itself (one rooted
// at the home directory) does not exempt it.
func DenyViewerRead(path, root string) error {
	home, _ := os.UserHomeDir()
	return denyViewerRead(path, root, viewerProtected(home, forgeCredentialsPath()))
}

func denyViewerRead(path, root string, protected []Credential) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	real := ResolveLoose(abs)
	realRoot := ""
	if strings.TrimSpace(root) != "" {
		realRoot = ResolveLoose(root)
	}
	info, statErr := os.Stat(real)
	for _, c := range protected {
		realStore := ResolveLoose(c.Path)
		hit := c.covers(abs) || c.covers(real) ||
			Credential{Path: realStore, Dir: c.Dir}.covers(real)
		if !hit && !c.Dir && statErr == nil {
			// A hard link, or a second mount of the same disk.
			if ci, err := os.Stat(realStore); err == nil && os.SameFile(info, ci) {
				hit = true
			}
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

// covers reports whether p is the credential location c or, for a directory,
// lies beneath it.
func (c Credential) covers(p string) bool {
	if p == c.Path {
		return true
	}
	return c.Dir && within(p, c.Path)
}
