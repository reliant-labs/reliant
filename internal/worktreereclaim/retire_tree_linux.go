//go:build linux

package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// retirementTree pins an inspected checkout's directory, never a pathname
// prefix that an external rename/symlink can redirect. This is containment and
// change detection, not a write freeze for arbitrary external processes.
type retirementTree struct {
	root    *os.File
	parent  *os.File
	name    string
	entries map[string]retirementEntry
	bytes   int64
	newest  time.Time
	// subtree marks a regenerable directory inside a live checkout (a
	// node_modules, build output) rather than a whole checkout: it holds no
	// git pointer, a .git anywhere in it is a nested repository and a hold,
	// and remove takes the directory itself last.
	subtree bool
	// path is the checkout's absolute path, for the git questions below.
	path string
	// tracked is every regular file HEAD tracks in the checkout and every
	// directory on the way to one, with its kind, read only when the
	// inventory first meets a never-listed name (trackedPath); head is the
	// commit it was read from.
	tracked     map[string]trackedKind
	head        string
	trackedRead bool
	// pending is the file remove unlinked last, held open until the next
	// inspection settles it; the inspection exempts this exact descriptor.
	pending *os.File
}

// maxRetirementEntries bounds one inventory. A checkout with its installs
// is well over 100k entries (reliant's frontends/web/node_modules alone is
// ~108k), so the 50k strip-walk budget this once shared refused exactly the
// worktrees that hold the disk. It bounds memory, not safety: every entry is
// still inspected, and hitting it holds the tree.
const maxRetirementEntries = 500_000

type retirementEntry struct {
	stat unix.Stat_t
}

func retirementOpen(dir int, name string, directory bool) (*os.File, error) {
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(dir, name, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func retirementRoot(path string) (*retirementTree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// NO_XDEV starts at the checkout's pinned parent: a worktree volume itself
	// can be mounted, but neither a mounted checkout nor any descendant can be.
	fd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(abs), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fd), filepath.Dir(abs))
	root, err := retirementOpen(fd, filepath.Base(abs), true)
	if err != nil {
		parent.Close()
		return nil, err
	}
	return &retirementTree{root: root, parent: parent, name: filepath.Base(abs), path: abs}, nil
}
func (t *retirementTree) close() { t.root.Close(); t.parent.Close() }
func (t *retirementTree) inspectionFiles() []*os.File {
	return []*os.File{t.root, t.parent, t.pending}
}
func retirementSame(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim && a.Nlink == b.Nlink
}
func retirementIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode
}

// installRecord is what a package manager wrote into the node_modules it
// populated, naming the packages it put there. It is the proof that a
// directory in node_modules is an install's output, reproduced from the
// lockfile, rather than something a person put there: a package.json alone
// proves nothing (anyone can keep a hand-made package, with its data/ or
// .env, in node_modules). No record, or a record that does not name the
// directory, keeps the never-list's hold.
type installRecord struct {
	// npm: the package paths in node_modules/.package-lock.json, npm's
	// hidden lockfile ("node_modules/pkg", "node_modules/a/node_modules/b"),
	// without workspace links.
	npm map[string]bool
	// pnpm: the virtual-store directory names (node_modules/.pnpm/<id>)
	// that the package ids node_modules/.pnpm/lock.yaml, pnpm's hidden
	// lockfile, names map to, each to its package's name; present only
	// when node_modules/.modules.yaml is a regular file too. A
	// virtual-store directory is an installed package only when this
	// names that exact directory for that package: the store's shape
	// alone is something anyone can make.
	pnpm map[string]string
}

// maxInstallRecordBytes bounds the hidden lockfile read; a larger one is no
// record (the tree holds).
const maxInstallRecordBytes = 64 << 20

// readInstallRecord reads the install record of the node_modules directory
// dir. Anything unreadable, oversized, malformed or not a regular file is no
// record. The record files are themselves inventoried, so validate catches
// one rewritten after inspection.
func readInstallRecord(dir *os.File) installRecord {
	var rec installRecord
	if b, ok := readRecordFile(dir, ".package-lock.json", true); ok {
		var lock struct {
			Packages map[string]struct {
				Link bool `json:"link"`
			} `json:"packages"`
		}
		if json.Unmarshal(b, &lock) == nil {
			rec.npm = map[string]bool{}
			for p, e := range lock.Packages {
				if !e.Link {
					rec.npm[p] = true
				}
			}
		}
	}
	if _, ok := readRecordFile(dir, ".modules.yaml", false); ok {
		if b, ok := readRecordFile(dir, ".pnpm/lock.yaml", true); ok {
			rec.pnpm = pnpmLockPackages(b)
		}
	}
	return rec
}

// pnpmLockPackages maps the virtual-store directory name of every package id
// a pnpm lockfile names in its packages and snapshots (lockfile v6+:
// "/name@1.0.0(peer@2.0.0)", "name@1.0.0") to the package's name. A
// malformed lockfile names nothing. Older (v5, "/name/1.0.0") keys, local
// ("file:") ids and ids whose directory pnpm hashes name nothing either, so
// their packages keep the never-list's hold.
func pnpmLockPackages(b []byte) map[string]string {
	var lock struct {
		Packages  map[string]yaml.Node `yaml:"packages"`
		Snapshots map[string]yaml.Node `yaml:"snapshots"`
	}
	if yaml.Unmarshal(b, &lock) != nil {
		return nil
	}
	dirs := map[string]string{}
	for _, keys := range []map[string]yaml.Node{lock.Packages, lock.Snapshots} {
		for key := range keys {
			if name, dir, ok := pnpmStoreDir(key); ok {
				dirs[dir] = name
			}
		}
	}
	return dirs
}

// pnpmMaxStoreDir is the longest virtual-store directory name pnpm writes
// unhashed (its virtual-store-dir-max-length default).
const pnpmMaxStoreDir = 120

// pnpmStoreDir is the package name in the lockfile key key and the
// virtual-store directory pnpm names after it, as pnpm's depPathToFilename
// does: the id with \/:*?"<>| as "+", and its peer suffix
// ("(a@1)(b@2(c@3))") with the closing ")" dropped and every "(", ")" and
// ")(" as "_". A key that is not "<name>@<version>" followed by an optional
// balanced, non-empty peer suffix, or whose directory pnpm hashes (too long,
// or not lowercase), is not ok.
func pnpmStoreDir(key string) (name, dir string, ok bool) {
	key = strings.TrimPrefix(key, "/")
	if strings.HasPrefix(key, "file:") {
		return "", "", false
	}
	id, peers := key, ""
	if i := strings.IndexByte(key, '('); i >= 0 {
		id, peers = key[:i], key[i:]
	}
	at := -1
	if len(id) > 1 {
		at = strings.IndexByte(id[1:], '@') + 1
	}
	if at <= 0 || at == len(id)-1 || strings.ContainsRune(id, ')') {
		return "", "", false
	}
	if !pnpmPeersWellFormed(peers) {
		return "", "", false
	}
	var b strings.Builder
	b.WriteString(pnpmStoreEscape(id))
	peers = strings.TrimSuffix(pnpmStoreEscape(peers), ")")
	for i := 0; i < len(peers); i++ {
		switch {
		case peers[i] == ')' && i+1 < len(peers) && peers[i+1] == '(':
			b.WriteByte('_')
			i++
		case peers[i] == '(' || peers[i] == ')':
			b.WriteByte('_')
		default:
			b.WriteByte(peers[i])
		}
	}
	dir = b.String()
	if len(dir) > pnpmMaxStoreDir || dir != strings.ToLower(dir) {
		return "", "", false
	}
	return id[:at], dir, true
}

// pnpmPeersWellFormed reports whether peers is empty or a sequence of
// balanced groups "(peer@version...)", each naming something: nothing
// outside the groups, and no empty group.
func pnpmPeersWellFormed(peers string) bool {
	depth := 0
	for i := 0; i < len(peers); i++ {
		switch peers[i] {
		case '(':
			if i+1 < len(peers) && (peers[i+1] == ')' || peers[i+1] == '(') {
				return false
			}
			depth++
		case ')':
			if depth == 0 {
				return false
			}
			depth--
		default:
			if depth == 0 {
				return false
			}
		}
	}
	return depth == 0
}

// pnpmStoreEscape replaces the characters pnpm replaces with "+" when it
// names a virtual-store directory after a package id.
func pnpmStoreEscape(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return '+'
		}
		return r
	}, s)
}

// readRecordFile opens name beneath dir without following any symlink and
// reports whether it is a regular file, reading it when read is set.
func readRecordFile(dir *os.File, name string, read bool) ([]byte, bool) {
	f, err := retirementOpen(int(dir.Fd()), name, false)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > maxInstallRecordBytes {
		return nil, false
	}
	if !read {
		return nil, true
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInstallRecordBytes+1))
	if err != nil || len(b) > maxInstallRecordBytes {
		return nil, false
	}
	return b, true
}

// segments is rel (tree-relative; "" is the tree's root) as path segments
// from the tree's parent when the tree is a node_modules shed on its own, so
// both inspections see the same "node_modules/..." path.
func (t *retirementTree) segments(rel string) []string {
	parts := splitSegments(rel)
	if t.subtree && t.name == "node_modules" {
		parts = append([]string{t.name}, parts...)
	}
	return parts
}

// installRoot reports whether the directory at rel is the outermost
// node_modules on its path: the one a package manager writes its record into.
func (t *retirementTree) installRoot(rel string) bool {
	parts := t.segments(rel)
	return len(parts) > 0 && slices.Index(parts, "node_modules") == len(parts)-1
}

// installedPackage reports whether the directory at rel is a package that
// the install recorded in rec put there: it holds a package.json file, and
// rec names it — npm's hidden lockfile lists its path, or it is a package in
// pnpm's virtual store (.pnpm/<name>@<version>/node_modules/<name>).
// Everything inside one is the install's output, reproduced from the
// lockfile, so a name on the never-list there (`data`, `.env`, `*.db`,
// `secrets`) is the package's own content and not the user's data. Packages
// ship such names routinely: @protobufjs/utf8 has tests/data and
// tsconfig-paths lib/__tests__/data, and one of them is in nearly every
// frontend's tree, so the never-list held every node_modules shed and every
// whole retirement of a checkout with an install (2026-11-06: zero sheds in
// 3,730 live-row reconciles on one machine with 108 GiB of node_modules). A
// never-listed name anywhere else in node_modules (its own data/, a directory
// no record names, a package.json-bearing directory a person made) still
// holds.
func (t *retirementTree) installedPackage(rel string, children []os.DirEntry, rec *installRecord) bool {
	if rec == nil {
		return false
	}
	parts := t.segments(rel)
	n := len(parts)
	var name string
	switch {
	case n >= 2 && parts[n-2] == "node_modules" && !strings.HasPrefix(parts[n-1], "@"):
		name = parts[n-1]
	case n >= 3 && parts[n-3] == "node_modules" && strings.HasPrefix(parts[n-2], "@"):
		name = parts[n-2] + "/" + parts[n-1]
	default:
		return false
	}
	manifest := false
	for _, c := range children {
		if c.Name() == "package.json" && c.Type().IsRegular() {
			manifest = true
			break
		}
	}
	if !manifest {
		return false
	}
	root := slices.Index(parts, "node_modules")
	under := parts[root:]
	if rec.npm[strings.Join(under, "/")] {
		return true
	}
	// pnpm: node_modules/.pnpm/<dir>/node_modules/<name>, where <dir> is
	// exactly the directory pnpm names after a package id its lockfile
	// names, and that id is of the package <name>.
	if len(under) != 4+len(strings.Split(name, "/")) || under[1] != ".pnpm" || under[3] != "node_modules" {
		return false
	}
	pkg, ok := rec.pnpm[under[2]]
	return ok && pkg == name
}

func (t *retirementTree) inventory(ctx context.Context) (map[string]retirementEntry, int64, time.Time, error) {
	entries := map[string]retirementEntry{}
	var bytes int64
	var newest time.Time
	var visit func(*os.File, string, bool, *installRecord) error
	// inPackage: dir is inside an installed package (installedPackage); rec:
	// the install record of the node_modules dir is in, if any.
	visit = func(dir *os.File, rel string, inPackage bool, rec *installRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
			return err
		}
		entries[rel] = retirementEntry{stat: st}
		when := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
		if when.After(newest) {
			newest = when
		}
		// Open a new description so enumeration offset never leaks across passes.
		reader, err := retirementOpen(int(dir.Fd()), ".", true)
		if err != nil {
			return err
		}
		children, err := reader.ReadDir(maxStripWalk + 1)
		reader.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(children) > maxStripWalk {
			return errors.New("retirement directory budget exceeded")
		}
		if t.installRoot(rel) {
			r := readInstallRecord(dir)
			rec = &r
		}
		inPackage = inPackage || t.installedPackage(rel, children, rec)
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maxRetirementEntries {
				return errors.New("retirement inventory budget exceeded")
			}
			name := child.Name()
			path := name
			if rel != "" {
				path = rel + "/" + name
			}
			if name == ".git" && (rel != "" || t.subtree) {
				return errors.New("nested repository in retirement tree")
			}
			var s unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), name, &s, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			if !inPackage && neverListed(path) && !t.trackedPath(ctx, path, s.Mode) {
				return fmt.Errorf("protected descendant: %s", path)
			}
			if s.Dev != st.Dev {
				return errors.New("retirement crosses filesystem")
			}
			switch s.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				if strings.Count(path, "/") >= 64 {
					return errors.New("retirement depth budget exceeded")
				}
				childDir, err := retirementOpen(int(dir.Fd()), name, true)
				if err != nil {
					return err
				}
				var opened unix.Stat_t
				err = unix.Fstat(int(childDir.Fd()), &opened)
				if err == nil && !retirementSame(s, opened) {
					err = errors.New("directory identity changed")
				}
				if err == nil {
					err = visit(childDir, path, inPackage, rec)
				}
				childDir.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				f, err := retirementOpen(int(dir.Fd()), name, false)
				if err != nil {
					return err
				}
				var opened unix.Stat_t
				err = unix.Fstat(int(f.Fd()), &opened)
				f.Close()
				if err != nil {
					return err
				}
				if !retirementSame(s, opened) {
					return errors.New("file identity changed")
				}
				// A file with other links (pnpm hard-links its store into every
				// node_modules) loses only this name: unlinkat never frees
				// content another link still names.
				entries[path] = retirementEntry{stat: s}
				if s.Nlink == 1 {
					bytes += s.Size
				}
			case unix.S_IFLNK:
				// Symlinks are never followed: remove unlinks the link itself,
				// which touches nothing it points at. Every node_modules has them
				// (.bin, pnpm's whole layout), so refusing them held every
				// worktree that had run an install.
				entries[path] = retirementEntry{stat: s}
			default:
				return errors.New("special file in retirement tree")
			}
			if name != ".git" {
				when := time.Unix(s.Mtim.Sec, s.Mtim.Nsec)
				if when.After(newest) {
					newest = when
				}
			}
		}
		return nil
	}
	if err := visit(t.root, "", false, nil); err != nil {
		return nil, 0, time.Time{}, err
	}
	return entries, bytes, newest, nil
}

// trackedPath reports whether rel is a regular file HEAD tracks in this
// checkout, or a directory holding one, AND is that same kind on disk (mode,
// from lstat). A symlink or submodule HEAD tracks under a never-listed name
// is never exempt, nor is a path whose kind on disk is not HEAD's: a
// `secrets` HEAD tracks as a file but that is a symlink (or a directory) here
// is the user's, whatever its name in the commit. The never-list protects the user's data: an
// ignored .env, a local database, a secrets directory nothing commits. A
// tracked deploy/secrets/ or internal/secrets/ package is source, in a commit
// the classifier proves is on the remote, and the classifier proves the
// working tree matches it; holding it held every checkout of every project
// that has one (2026-11-07: 174 of 470 live worktrees on one machine, all of
// control-plane's and reliant's, so no workspace that contains them could
// ever be retired). Only HEAD's own paths are exempt: an ignored or
// untracked file inside a tracked secrets/ is still a protected descendant.
// The exemption is tied to the commit it was read from (validate), so a
// HEAD that moves after inspection holds the tree. A checkout git cannot
// read exempts nothing. A regenerable subtree never consults it.
func (t *retirementTree) trackedPath(ctx context.Context, rel string, mode uint32) bool {
	if t.subtree {
		return false
	}
	if !t.trackedRead {
		t.trackedRead = true
		t.head, t.tracked = trackedInHead(ctx, t.path)
	}
	switch t.tracked[rel] {
	case trackedFile:
		return mode&unix.S_IFMT == unix.S_IFREG
	case trackedDir:
		return mode&unix.S_IFMT == unix.S_IFDIR
	default:
		return false
	}
}

// trackedKind is what HEAD's tree holds at a path, for trackedPath.
type trackedKind byte

const (
	trackedNone trackedKind = iota
	trackedFile
	trackedDir
)

// trackedInHead is the commit HEAD names in checkout and the set of paths its
// tree holds, directories on the way to each included; "" and nil when git
// cannot say.
func trackedInHead(ctx context.Context, checkout string) (string, map[string]trackedKind) {
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head == "" {
		return "", nil
	}
	out, err := gitRaw(ctx, checkout, nil, "ls-tree", "-r", "-z", "--full-tree", head)
	if err != nil {
		return "", nil
	}
	return head, trackedSet(out)
}

// trackedSet is the regular files of NUL-separated `git ls-tree -r -z`
// output (`<mode> <type> <object>\t<path>`; modes 100644 and 100755) and
// every directory on the way to each. Symlinks (120000) and submodules
// (160000) are left out: what they name is not in the commit.
func trackedSet(lsTree []byte) map[string]trackedKind {
	tracked := map[string]trackedKind{}
	for _, rec := range strings.Split(string(lsTree), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok || p == "" {
			continue
		}
		if mode, _, _ := strings.Cut(meta, " "); mode != "100644" && mode != "100755" {
			continue
		}
		tracked[p] = trackedFile
		for d := path.Dir(p); d != "" && d != "." && tracked[d] == trackedNone; d = path.Dir(d) {
			tracked[d] = trackedDir
		}
	}
	return tracked
}

func retirementMatchesPath(t *retirementTree, path string) (bool, error) {
	other, err := retirementRoot(path)
	if err != nil {
		return false, err
	}
	defer other.close()
	var a, b unix.Stat_t
	if err := unix.Fstat(int(t.root.Fd()), &a); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(other.root.Fd()), &b); err != nil {
		return false, err
	}
	return retirementIdentity(a, b), nil
}

func inspectRetirementTree(ctx context.Context, path string) (*retirementTree, error) {
	t, err := retirementRoot(path)
	if err != nil {
		return nil, err
	}
	t.entries, t.bytes, t.newest, err = t.inventory(ctx)
	if err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}
func (t *retirementTree) validate(ctx context.Context) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(t.parent.Fd()), t.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementIdentity(named, t.entries[""].stat) {
		return errors.New("checkout pathname was replaced")
	}
	if t.head != "" {
		// Tracked paths were exempted as of t.head; a checkout that has moved
		// since is not the one the exemption describes.
		if head, err := git(ctx, t.path, nil, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || head != t.head {
			return errors.New("checkout HEAD changed since inspection")
		}
	}
	entries, _, _, err := t.inventory(ctx)
	if err != nil {
		return err
	}
	if len(entries) != len(t.entries) {
		return errors.New("retirement contents changed")
	}
	for p, e := range t.entries {
		if got, ok := entries[p]; !ok || !retirementSame(e.stat, got.stat) {
			return fmt.Errorf("retirement entry changed: %s", p)
		}
	}
	return nil
}

// remove only touches the inventoried directory via anchored descriptors. Any
// unexpected name/type/mount/identity is a hold, never a RemoveAll fallback.
// Caller must preserve its journal on partial progress; deletion isn't atomic.
func (t *retirementTree) remove(ctx context.Context, check func() error) error {
	if err := t.validate(ctx); err != nil {
		return err
	}
	paths := make([]string, 0, len(t.entries))
	for p := range t.entries {
		if p != "" && p != ".git" {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di > dj
		}
		return paths[i] < paths[j]
	})
	// A regular file stays open after its unlink, until the next inspection
	// has run (settle): a process that opened it between the last inspection
	// and the unlink has either written by then (its size or mtime moved) or
	// still holds it (the inspection sees the deleted file under the tree).
	// Either way its bytes are put back and retirement stops. A name created
	// after inspection is never inventoried, so the directory holding it
	// fails AT_REMOVEDIR (ENOTEMPTY) and survives with it.
	var pending *unlinkedFile
	settle := func(checkErr error) error {
		u := pending
		if u == nil {
			return checkErr
		}
		pending, t.pending = nil, nil
		defer u.dir.Close()
		defer u.file.Close()
		var now unix.Stat_t
		err := unix.Fstat(int(u.file.Fd()), &now)
		if err == nil && checkErr == nil && now.Size == u.stat.Size && now.Mtim == u.stat.Mtim {
			return nil
		}
		if rerr := u.restore(); rerr != nil {
			return fmt.Errorf("%s changed at its deletion and could not be put back: %w", u.rel, rerr)
		}
		if checkErr != nil {
			return checkErr
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("file written at its deletion, put back: %s", u.rel)
	}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return settle(err)
		}
		if err := settle(check()); err != nil {
			return err
		}
		dir, err := retirementOpen(int(t.root.Fd()), filepath.ToSlash(filepath.Dir(p)), true)
		if err != nil {
			return err
		}
		parentRel := filepath.ToSlash(filepath.Dir(p))
		if parentRel == "." {
			parentRel = ""
		}
		var parentStat unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &parentStat); err != nil || !retirementIdentity(parentStat, t.entries[parentRel].stat) {
			dir.Close()
			return errors.New("ancestor directory changed at deletion boundary")
		}
		base := filepath.Base(p)
		e := t.entries[p]
		var st unix.Stat_t
		err = unix.Fstatat(int(dir.Fd()), base, &st, unix.AT_SYMLINK_NOFOLLOW)
		isDir := e.stat.Mode&unix.S_IFMT == unix.S_IFDIR
		isLink := e.stat.Mode&unix.S_IFMT == unix.S_IFLNK
		if err == nil && ((isDir && !retirementIdentity(e.stat, st)) || (!isDir && !retirementSame(e.stat, st))) {
			err = errors.New("entry changed at deletion boundary")
		}
		var held *os.File
		if err == nil && !isLink {
			// Openat2 supplies NO_XDEV for directories including same-device binds.
			f, openErr := retirementOpen(int(dir.Fd()), base, isDir)
			if openErr != nil {
				err = openErr
			} else {
				var opened unix.Stat_t
				err = unix.Fstat(int(f.Fd()), &opened)
				if err == nil && !retirementIdentity(st, opened) {
					err = errors.New("entry replaced while opening")
				}
				if err == nil && !isDir {
					held = f
				} else {
					f.Close()
				}
			}
		}
		if err == nil {
			flags := 0
			if isDir {
				flags = unix.AT_REMOVEDIR
			}
			err = unix.Unlinkat(int(dir.Fd()), base, flags)
		}
		if err == nil && held != nil {
			pending = &unlinkedFile{dir: dir, base: base, rel: p, file: held, stat: e.stat}
			t.pending = held
			continue
		}
		if held != nil {
			held.Close()
		}
		dir.Close()
		if err != nil {
			return err
		}
	}
	if err := settle(check()); err != nil {
		return err
	}
	if t.subtree {
		return t.removeRoot()
	}
	// Keep .git until all other entries have been removed, so partial failures
	// remain registered/recoverable. Never remove an unexpected final entry.
	reader, err := retirementOpen(int(t.root.Fd()), ".", true)
	if err != nil {
		return err
	}
	names, err := reader.Readdirnames(2)
	reader.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != 1 || names[0] != ".git" {
		return errors.New("unexpected entries remain after retirement")
	}
	var pointer unix.Stat_t
	if err := unix.Fstatat(int(t.root.Fd()), ".git", &pointer, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementSame(pointer, t.entries[".git"].stat) {
		return errors.New("git pointer changed")
	}
	if err := unix.Unlinkat(int(t.root.Fd()), ".git", 0); err != nil {
		return err
	}
	return t.removeRoot()
}

// unlinkedFile is a regular file remove has unlinked and still holds open:
// its bytes stay reachable until the next inspection proves nobody wrote it.
type unlinkedFile struct {
	dir  *os.File // the inventoried directory that named it
	base string
	rel  string
	file *os.File
	stat unix.Stat_t // as inventoried
}

// restore writes the unlinked file's current bytes back under its name in
// the directory that held it, or beside it when something new took the name
// (that newcomer is never inventoried, so it is preserved too). Never
// following a link, never replacing an existing name.
func (u *unlinkedFile) restore() error {
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
	mode := u.stat.Mode & 0o7777
	name := u.base
	fd, err := unix.Openat(int(u.dir.Fd()), name, flags, mode)
	if errors.Is(err, unix.EEXIST) {
		name = u.base + ".retained"
		fd, err = unix.Openat(int(u.dir.Fd()), name, flags, mode)
	}
	if err != nil {
		return err
	}
	dst := os.NewFile(uintptr(fd), name)
	err = unix.Fchmod(fd, mode)
	if err == nil {
		_, err = io.Copy(dst, io.NewSectionReader(u.file, 0, math.MaxInt64))
	}
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return err
}

// removeRoot removes the now-empty inventoried directory itself, only while
// its name still names the directory that was inspected.
func (t *retirementTree) removeRoot() error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(t.parent.Fd()), t.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementIdentity(named, t.entries[""].stat) {
		return errors.New("checkout name replaced during retirement")
	}
	return unix.Unlinkat(int(t.parent.Fd()), t.name, unix.AT_REMOVEDIR)
}

// inspectRegenerableTree inventories a regenerable directory inside a live
// checkout (node_modules, build output, a tool cache) for shedding. It is the
// retirement inventory with the same holds — a protected descendant, a nested
// repository, a mount, a special file — except that the directory has no git
// pointer of its own and remove takes the directory itself.
func inspectRegenerableTree(ctx context.Context, path string) (*retirementTree, error) {
	t, err := retirementRoot(path)
	if err != nil {
		return nil, err
	}
	t.subtree = true
	t.entries, t.bytes, t.newest, err = t.inventory(ctx)
	if err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}
