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

// viewerXDGDirs are the home-relative directories an XDG variable moves: a
// store under one of them is also refused at the same place under the
// variable's directory (gh's login at $XDG_CONFIG_HOME/gh/hosts.yml).
var viewerXDGDirs = []struct{ prefix, env string }{
	{".config/", "XDG_CONFIG_HOME"},
	{".local/share/", "XDG_DATA_HOME"},
}

// viewerEnvStores are credential stores a tool's own variable relocates. A
// variable that is unset, or not an absolute path, adds nothing; the store's
// default location stays protected either way.
var viewerEnvStores = []struct {
	env  string
	file string // the credential file inside the directory env names; "" when env names the store itself
	dir  bool
}{
	{"GH_CONFIG_DIR", "", true},
	{"GNUPGHOME", "", true},
	{"CLOUDSDK_CONFIG", "", true},
	{"AWS_SHARED_CREDENTIALS_FILE", "", false},
	{"DOCKER_CONFIG", "config.json", false},
	{"CLAUDE_CONFIG_DIR", ".credentials.json", false},
	{"CODEX_HOME", "auth.json", false},
}

// viewerProtected returns the credential stores the viewer refuses for a user
// whose home is home, in an environment read through getenv: each store at its
// default location under home and wherever the environment relocates it.
func viewerProtected(home, forgeCredentials string, getenv func(string) string) []Credential {
	var out []Credential
	add := func(rel string, dir bool) {
		if home != "" {
			out = appendCredential(out, Credential{Path: filepath.Join(home, rel), Dir: dir})
		}
		for _, x := range viewerXDGDirs {
			if base := getenv(x.env); strings.HasPrefix(rel, x.prefix) && filepath.IsAbs(base) {
				out = appendCredential(out, Credential{Path: filepath.Join(base, strings.TrimPrefix(rel, x.prefix)), Dir: dir})
			}
		}
	}
	for _, e := range protectedHome {
		if files, ok := viewerNarrowed[e.rel]; ok {
			for _, f := range files {
				add(f, false)
			}
			continue
		}
		add(e.rel, e.dir)
	}
	for _, e := range viewerOnlyHome {
		add(e.rel, e.dir)
	}
	for _, s := range viewerEnvStores {
		p := getenv(s.env)
		if !filepath.IsAbs(p) {
			continue
		}
		if s.file != "" {
			p = filepath.Join(p, s.file)
		}
		out = appendCredential(out, Credential{Path: p, Dir: s.dir})
	}
	// KUBECONFIG is a list of files, each a login.
	for _, p := range filepath.SplitList(getenv("KUBECONFIG")) {
		if filepath.IsAbs(p) {
			out = appendCredential(out, Credential{Path: p})
		}
	}
	for _, p := range reliantCredentialFiles(home, forgeCredentials) {
		out = appendCredential(out, Credential{Path: p})
	}
	return out
}

// viewerGetenv reads the variables that locate credential stores. They are
// the user's tools' configuration, not Reliant's.
var viewerGetenv = os.Getenv //nolint:forbidigo // locates third-party credential stores; not Reliant configuration

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
	return denyViewerRead(path, root, viewerProtected(home, forgeCredentialsPath(), viewerGetenv))
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
	return checkViewerFile(f, path, root, viewerProtected(home, forgeCredentialsPath(), viewerGetenv))
}

func checkViewerFile(f *os.File, path, root string, protected []Credential) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	abs, real := viewerPaths(path)
	if ri, err := viewerStat(real); err != nil || !os.SameFile(info, ri) {
		return fmt.Errorf("%w (%s changed while it was opened)", ErrViewerCredential, path)
	}
	return checkViewerRead(abs, real, root, info, protected)
}

func denyViewerRead(path, root string, protected []Credential) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, real := viewerPaths(path)
	info, err := viewerStat(real)
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
//
// Pathnames alone cannot decide it: a case-insensitive volume (macOS by
// default) opens ~/.SSH/id_rsa as ~/.ssh/id_rsa, and symlink resolution keeps
// the spelling it was given. So besides the string checks, a path is in a
// directory store when the store's directory is, by file identity, the path
// or one of its parents, and it is a file store when it is that file.
func checkViewerRead(abs, real, root string, info os.FileInfo, protected []Credential) error {
	realRoot := ""
	var rootInfo os.FileInfo
	if strings.TrimSpace(root) != "" {
		realRoot = ResolveLoose(root)
		rootInfo, _ = viewerStat(realRoot)
	}
	chain := identityChain(real)
	for _, c := range protected {
		realStore := ResolveLoose(c.Path)
		storeInfo, _ := viewerStat(realStore)
		hit := c.covers(abs) || c.covers(real) ||
			Credential{Path: realStore, Dir: c.Dir}.covers(real)
		if !hit && storeInfo != nil && holdsByIdentity(chain, info, storeInfo, c.Dir) {
			// The store under another spelling, a hard link, or a second
			// mount of the same disk. Where it sits says nothing about what
			// it is, so no exemption below applies.
			return fmt.Errorf("%w (%s)", ErrViewerCredential, c.Path)
		}
		// Where the store sits, its own name unresolved: ~/.ssh stays ~/.ssh
		// when it links into a dotfiles repo, and becomes /data/u/.ssh when
		// the home directory itself is a link to /data/u.
		storeAt := filepath.Join(ResolveLoose(filepath.Dir(c.Path)), filepath.Base(c.Path))
		exempt := realRoot != "" && within(real, realRoot) && !withinByIdentity(storeAt, realRoot, rootInfo)
		if !hit && c.Dir && storeInfo != nil && storeInfo.IsDir() && info != nil && info.Mode().IsRegular() {
			switch {
			case mayHaveOtherNames(info):
				// A second name for a file reached through the store,
				// directly or by a symlink in it, wherever the name sits.
				if storeHolds(realStore, info, false) {
					return fmt.Errorf("%w (%s)", ErrViewerCredential, c.Path)
				}
			case !exempt:
				// A single-name file is in the store only as the target of
				// a symlink in it (~/.ssh/id_rsa -> /keys/id_rsa). A file
				// of the workspace that a store links to is the dotfiles
				// case below, so the walk is spared there.
				hit = storeHolds(realStore, info, true)
			}
		}
		if !hit || exempt {
			continue
		}
		return fmt.Errorf("%w (%s)", ErrViewerCredential, c.Path)
	}
	return nil
}

// viewerStat is os.Stat; tests replace it to stand in for a case-insensitive
// volume, which the hosts that run them do not have.
var viewerStat = os.Stat

// identityChain returns the file identities of p and each of its parents, p
// first. A component that does not exist is skipped.
func identityChain(p string) []os.FileInfo {
	var out []os.FileInfo
	for {
		if fi, err := viewerStat(p); err == nil {
			out = append(out, fi)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return out
		}
		p = parent
	}
}

// holdsByIdentity reports whether the file whose identity chain is chain (and
// whose own info is info, nil when it does not exist) is the file store, or
// lies in the directory store, whose info is store.
func holdsByIdentity(chain []os.FileInfo, info, store os.FileInfo, dir bool) bool {
	if !dir {
		return info != nil && os.SameFile(info, store)
	}
	for _, fi := range chain {
		if os.SameFile(fi, store) {
			return true
		}
	}
	return false
}

// withinByIdentity is within, also true when root is, by file identity, p or
// one of its parents.
func withinByIdentity(p, root string, rootInfo os.FileInfo) bool {
	if within(p, root) {
		return true
	}
	if rootInfo == nil {
		return false
	}
	for _, fi := range identityChain(p) {
		if os.SameFile(fi, rootInfo) {
			return true
		}
	}
	return false
}

// storeHolds reports whether target is a file reached through the directory
// store: a regular file in it, a file a symlink in it points at, or one of
// those beneath a directory a symlink in it points at. Directories are
// visited once by identity, so a symlink cycle ends. linkedOnly counts only
// files reached through a symlink, for a target with one name: a regular file
// in the store's own tree has that tree's path, which the string checks saw.
func storeHolds(store string, target os.FileInfo, linkedOnly bool) bool {
	var seen []os.FileInfo
	var walk func(dir string, linked bool) bool
	walk = func(dir string, linked bool) bool {
		di, err := viewerStat(dir)
		if err != nil || !di.IsDir() {
			return false
		}
		for _, s := range seen {
			if os.SameFile(s, di) {
				return false
			}
		}
		seen = append(seen, di)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			switch {
			case e.IsDir():
				if walk(p, linked) {
					return true
				}
			case e.Type()&fs.ModeSymlink != 0:
				fi, err := viewerStat(p)
				if err != nil {
					continue
				}
				if fi.IsDir() {
					if walk(p, true) {
						return true
					}
					continue
				}
				if os.SameFile(fi, target) {
					return true
				}
			case e.Type().IsRegular():
				if linkedOnly && !linked {
					continue
				}
				if fi, err := e.Info(); err == nil && os.SameFile(fi, target) {
					return true
				}
			}
		}
		return false
	}
	return walk(store, false)
}

// covers reports whether p is the credential location c or, for a directory,
// lies beneath it.
func (c Credential) covers(p string) bool {
	if p == c.Path {
		return true
	}
	return c.Dir && within(p, c.Path)
}
