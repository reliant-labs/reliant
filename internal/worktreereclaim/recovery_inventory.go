// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const recoveryMaxFiles = 10000
const recoveryMaxBytes = 64 << 20
const recoveryMaxObject = 4 << 20
const recoveryMaxTips = 128

// RecoveryEntry names exact raw Git bytes, not a checkout-filtered file. Path is
// standard base64 of the slash-separated path bytes, including non-UTF8 names.
type RecoveryEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	OID  string `json:"oid"`
}

type recoveryInventory struct {
	OmittedDigest string
	Head          string
	Branch        string
	Format        string
	Index         string
	Disk          string
	Tips          []string
	Untracked     []string
	Entries       []RecoveryEntry
	IndexEntries  []RecoveryEntry
}

func recoveryPath(raw string) error {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\x00\\") || len(raw) > 4096 || strings.Count(raw, "/") > 64 {
		return fmt.Errorf("recovery: unsupported path encoding")
	}
	for _, p := range strings.Split(raw, "/") {
		if p == "" || p == "." || p == ".." || strings.EqualFold(p, ".git") || strings.Contains(p, ":") || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
			return fmt.Errorf("recovery: unsafe path")
		}
	}
	return nil
}

func recoveryEntries(raw []byte) ([]RecoveryEntry, error) {
	var entries []RecoveryEntry
	seen := map[string]bool{}
	spellings := map[string]string{}
	for _, row := range bytes.Split(raw, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("recovery: invalid tree/index enumeration")
		}
		if err := recoveryPath(string(path)); err != nil {
			return nil, err
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			return nil, fmt.Errorf("recovery: unsupported file mode")
		}
		if fields[2] != "0" && fields[1] != "blob" {
			return nil, fmt.Errorf("recovery: non-stage-zero index")
		}
		oid := fields[1]
		if fields[1] == "blob" {
			oid = fields[2]
		}
		if !recoveryOID(oid) {
			return nil, fmt.Errorf("recovery: invalid object identity")
		}
		parts := strings.Split(string(path), "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			key := strings.ToLower(prefix)
			if old, ok := spellings[key]; ok && old != prefix {
				return nil, fmt.Errorf("recovery: case-colliding path prefix")
			}
			spellings[key] = prefix
		}
		key := strings.ToLower(string(path))
		if seen[key] {
			return nil, fmt.Errorf("recovery: duplicate/case-colliding path")
		}
		seen[key] = true
		entries = append(entries, RecoveryEntry{base64.StdEncoding.EncodeToString(path), fields[0], oid})
		if len(entries) > recoveryMaxFiles {
			return nil, fmt.Errorf("recovery: file budget exceeded")
		}
	}
	for p := range seen {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return nil, fmt.Errorf("recovery: file-directory path collision")
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func recoveryOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b)*2 == len(s) && s == strings.ToLower(s)
}

// Only ordinary v2/v3 stage-0 indexes with the optional cache-tree extension
// are supported. Ignoring flags here would lose assume-unchanged/skip-worktree,
// intent-to-add, sparse/split index or extension state during restore.
func recoveryCheckIndex(b []byte, hashBytes int) error {
	if len(b) < 12+hashBytes || string(b[:4]) != "DIRC" {
		return fmt.Errorf("recovery: malformed index")
	}
	v := binary.BigEndian.Uint32(b[4:8])
	if v != 2 && v != 3 {
		return fmt.Errorf("recovery: unsupported index version")
	}
	n := int(binary.BigEndian.Uint32(b[8:12]))
	if n > recoveryMaxFiles {
		return fmt.Errorf("recovery: index budget exceeded")
	}
	off := 12
	for i := 0; i < n; i++ {
		start := off
		fixed := 42 + hashBytes
		if off+fixed > len(b)-hashBytes {
			return fmt.Errorf("recovery: truncated index")
		}
		flags := binary.BigEndian.Uint16(b[off+fixed-2 : off+fixed])
		if flags&0xf000 != 0 {
			return fmt.Errorf("recovery: unsupported index flags/stages")
		}
		off += fixed
		end := bytes.IndexByte(b[off:len(b)-hashBytes], 0)
		if end < 0 {
			return fmt.Errorf("recovery: unterminated index path")
		}
		off += end + 1
		off = start + ((off - start + 7) &^ 7)
	}
	for off < len(b)-hashBytes {
		if off+8 > len(b)-hashBytes {
			return fmt.Errorf("recovery: truncated index extension")
		}
		name := string(b[off : off+4])
		size := int(binary.BigEndian.Uint32(b[off+4 : off+8]))
		off += 8
		if name != "TREE" || size > len(b)-hashBytes-off {
			return fmt.Errorf("recovery: unsupported index extension")
		}
		off += size
	}
	if off != len(b)-hashBytes {
		return fmt.Errorf("recovery: invalid index length")
	}
	return nil
}

func recoverySupported(ctx context.Context, path string) (string, string, string, error) {
	top, err := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", err
	}
	if !samePath(top, path) {
		return "", "", "", fmt.Errorf("recovery: checkout root required")
	}
	dotGit, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || dotGit.Mode()&os.ModeSymlink != 0 {
		return "", "", "", fmt.Errorf("recovery: unsupported Git directory entry")
	}
	gd, err := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", "", err
	}
	cd, err := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", "", err
	}
	for _, root := range []string{gd, cd} {
		for _, name := range []string{"MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "BISECT_START", "rebase-apply", "rebase-merge", "sequencer", "index.lock", "HEAD.lock", "shallow", "info/grafts", "objects/info/alternates"} {
			if _, e := os.Lstat(filepath.Join(root, name)); e == nil {
				return "", "", "", fmt.Errorf("recovery: unsupported Git operation/storage state (%s)", name)
			} else if !os.IsNotExist(e) {
				return "", "", "", fmt.Errorf("recovery: cannot inspect Git state")
			}
		}
	}
	cfg, err := recoveryGitText(ctx, path, nil, nil, "config", "--local", "--list")
	if err != nil {
		return "", "", "", err
	}
	for _, line := range strings.Split(cfg, "\n") {
		k, v, _ := strings.Cut(strings.ToLower(line), "=")
		if strings.HasPrefix(k, "filter.") || strings.HasSuffix(k, ".promisor") || strings.HasSuffix(k, ".partialclonefilter") || k == "extensions.partialclone" || k == "core.sparsecheckout" && v != "false" || k == "core.splitindex" && v != "false" || k == "core.attributesfile" || k == "core.worktree" {
			return "", "", "", fmt.Errorf("recovery: unsupported Git configuration")
		}
	}
	refs, err := recoveryGitText(ctx, path, nil, nil, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return "", "", "", err
	}
	if refs != "" {
		return "", "", "", fmt.Errorf("recovery: replacement objects unsupported")
	}
	format, err := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return "", "", "", err
	}
	if format != "sha1" && format != "sha256" {
		return "", "", "", fmt.Errorf("recovery: unsupported object format")
	}
	return gd, cd, format, nil
}

func recoveryInternalRef(ref string) bool {
	if strings.HasPrefix(ref, "refs/reliant/recovery-pins/") || strings.HasPrefix(ref, "refs/heads/reliant-recovery/") {
		return true
	}
	if strings.HasPrefix(ref, "refs/remotes/") {
		parts := strings.SplitN(strings.TrimPrefix(ref, "refs/remotes/"), "/", 2)
		return len(parts) == 2 && strings.HasPrefix(parts[1], "reliant-recovery/")
	}
	return false
}
func recoveryMetadataInternal(path string) bool {
	i := strings.Index(path, "/refs/")
	return i >= 0 && recoveryInternalRef(path[i+1:])
}

func recoveryHistory(ctx context.Context, path, gd, cd string) ([]string, error) {
	tips := map[string]bool{}
	add := func(oid string) error {
		if !recoveryOID(oid) {
			return fmt.Errorf("recovery: invalid history identity")
		}
		if strings.Trim(oid, "0") == "" {
			return nil
		}
		tips[oid] = true
		if len(tips) > recoveryMaxTips {
			return fmt.Errorf("recovery: history tip budget exceeded")
		}
		return nil
	}
	raw, err := recoveryGitText(ctx, path, nil, nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		p := strings.Fields(line)
		if len(p) != 2 {
			return nil, fmt.Errorf("recovery: incomplete refs")
		}
		if recoveryInternalRef(p[1]) {
			continue
		}
		oid, e := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--verify", p[1]+"^{commit}")
		if e != nil {
			return nil, fmt.Errorf("recovery: noncommit/unreadable history ref")
		}
		if e = add(oid); e != nil {
			return nil, e
		}
	}
	head, err := recoveryGitText(ctx, path, nil, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	if err = add(head); err != nil {
		return nil, err
	}
	// Read all reflogs, including old values, not just tips that remain on refs.
	// Per-worktree HEAD/private refs live outside the shared refs namespace.
	roots := []string{filepath.Join(cd, "logs"), filepath.Join(gd, "logs"), filepath.Join(cd, "worktrees")}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		seen[root] = true
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) && p == root {
				return nil
			}
			if e != nil {
				return fmt.Errorf("recovery: history enumeration failed")
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("recovery: symlinked history metadata")
			}
			if d.IsDir() {
				if recoveryMetadataInternal(filepath.ToSlash(p)) {
					return filepath.SkipDir
				}
				return nil
			}
			if recoveryMetadataInternal(filepath.ToSlash(p)) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			s := filepath.ToSlash(rel)
			isLog := strings.Contains(filepath.ToSlash(root), "/logs") || strings.Contains(s, "/logs/")
			isRef := strings.HasSuffix(s, "/HEAD") || strings.Contains(s, "/refs/")
			if !isLog && !isRef {
				return nil
			}
			if d.Type() != 0 {
				return fmt.Errorf("recovery: unsupported history metadata")
			}
			info, e := d.Info()
			if e != nil || info.Size() > recoveryMaxObject {
				return fmt.Errorf("recovery: history metadata budget")
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return fmt.Errorf("recovery: unreadable history metadata")
			}
			if isLog {
				for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
					if line == "" {
						continue
					}
					f := strings.Fields(line)
					if len(f) < 2 {
						return fmt.Errorf("recovery: malformed reflog")
					}
					if e = add(f[0]); e != nil {
						return e
					}
					if e = add(f[1]); e != nil {
						return e
					}
				}
				return nil
			}
			v := strings.TrimSpace(string(b))
			if strings.HasPrefix(v, "ref: ") {
				v, e = recoveryGitText(ctx, path, nil, nil, "rev-parse", "--verify", strings.TrimPrefix(v, "ref: ")+"^{commit}")
				if e != nil {
					return fmt.Errorf("recovery: unreadable worktree ref")
				}
			}
			return add(v)
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(tips))
	for t := range tips {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

func recoveryInventoryAt(ctx context.Context, path, tmp string) (recoveryInventory, error) {
	var out recoveryInventory
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return out, err
	}
	rootInfo, err := os.Lstat(path)
	if err != nil || !rootInfo.IsDir() {
		return out, fmt.Errorf("recovery: checkout must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return out, fmt.Errorf("recovery: symlinked checkout ancestor unsupported")
	}
	gd, cd, format, err := recoverySupported(ctx, path)
	if err != nil {
		return out, err
	}
	out.Format = format
	out.Head, err = recoveryGitText(ctx, path, nil, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return out, err
	}
	// symbolic-ref failure distinguishes detached HEAD without treating a branch
	// label as an executable rev expression. The HEAD object remains authoritative.
	headBytes, err := os.ReadFile(filepath.Join(gd, "HEAD"))
	if err != nil {
		return out, fmt.Errorf("recovery: unreadable HEAD")
	}
	if bytes.HasPrefix(headBytes, []byte("ref: ")) {
		out.Branch = strings.TrimSpace(strings.TrimPrefix(string(headBytes), "ref: "))
	}
	out.Tips, err = recoveryHistory(ctx, path, gd, cd)
	if err != nil {
		return out, err
	}
	index, err := os.ReadFile(filepath.Join(gd, "index"))
	if err != nil {
		return out, fmt.Errorf("recovery: unreadable real index")
	}
	hashBytes := 20
	if format == "sha256" {
		hashBytes = 32
	}
	if err = recoveryCheckIndex(index, hashBytes); err != nil {
		return out, err
	}
	idx := filepath.Join(tmp, "saved-index")
	if err = os.WriteFile(idx, index, 0600); err != nil {
		return out, err
	}
	env := []string{"GIT_INDEX_FILE=" + idx}
	raw, err := recoveryCaptureGit(ctx, path, env, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return out, err
	}
	out.IndexEntries, err = recoveryEntries(raw)
	if err != nil {
		return out, err
	}
	out.Index, err = recoveryGitText(ctx, path, env, nil, "write-tree")
	if err != nil {
		return out, err
	}
	ignored, err := recoveryCaptureGit(ctx, path, nil, nil, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return out, err
	}
	// A build/package declaration does not prove that ignored leaves are
	// reproducible: dist/notes.txt may be the user's only copy. Until a positive
	// immutable provenance mechanism exists, even ordinary dependency/build
	// directories hold instead of omitting their bytes from recovery.
	if len(ignored) > 0 {
		return out, fmt.Errorf("recovery: ignored content has no proven regeneration source")
	}
	walkCount := 0
	untracked, err := recoveryCaptureGit(ctx, path, nil, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return out, err
	}
	paths := map[string]bool{}
	for _, e := range out.IndexEntries {
		p, _ := base64.StdEncoding.DecodeString(e.Path)
		paths[string(p)] = true
	}
	for _, p := range bytes.Split(untracked, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		if err = recoveryPath(string(p)); err != nil {
			return out, err
		}
		paths[string(p)] = true
		out.Untracked = append(out.Untracked, base64.StdEncoding.EncodeToString(p))
	}
	sort.Strings(out.Untracked)
	// Walk independently of Git's status caches to reject unknown nested repos,
	// special files and ignored/empty data rather than silently omitting them.
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return fmt.Errorf("recovery: filesystem enumeration failed")
		}
		if p == path {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		walkCount++
		if walkCount > recoveryMaxFiles*2 {
			return fmt.Errorf("recovery: filesystem enumeration budget")
		}
		rel, e := filepath.Rel(path, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".git" {
			return fmt.Errorf("recovery: nested checkout requires independent capture")
		}
		if err := recoveryPath(rel); err != nil {
			return err
		}
		if d.IsDir() {
			entries, e := os.ReadDir(p)
			if e != nil {
				return fmt.Errorf("recovery: unreadable directory")
			}
			if len(entries) == 0 {
				return fmt.Errorf("recovery: empty directory not representable")
			}
			return nil
		}
		if !paths[rel] {
			return fmt.Errorf("recovery: unknown ignored/filesystem content")
		}
		if !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
			return fmt.Errorf("recovery: special filesystem content")
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	// V1 has no supported omission provenance: this is always the empty set.
	out.OmittedDigest = recoveryDigest([]RecoveryEntry{})
	list := make([]string, 0, len(paths))
	for p := range paths {
		list = append(list, p)
	}
	sort.Strings(list)
	if len(list) > recoveryMaxFiles {
		return out, fmt.Errorf("recovery: file budget exceeded")
	}
	total := 0
	seen := map[string]bool{}
	for _, p := range list {
		if err = recoveryPath(p); err != nil {
			return out, err
		}
		fold := strings.ToLower(p)
		if seen[fold] {
			return out, fmt.Errorf("recovery: case collision")
		}
		seen[fold] = true
		full := filepath.Join(path, filepath.FromSlash(p))
		st, e := os.Lstat(full)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return out, fmt.Errorf("recovery: unreadable file")
		}
		// Lstat each ancestor: never follow a symlink directory, even for tracked paths.
		for parent := filepath.Dir(full); parent != path; parent = filepath.Dir(parent) {
			s, e := os.Lstat(parent)
			if e != nil || !s.IsDir() {
				return out, fmt.Errorf("recovery: unsafe file ancestor")
			}
		}
		var b []byte
		mode := "100644"
		if st.Mode()&os.ModeSymlink != 0 {
			link, e := os.Readlink(full)
			if e != nil {
				return out, e
			}
			b = []byte(link)
			mode = "120000"
		} else if st.Mode().IsRegular() {
			if st.Size() > recoveryMaxObject {
				return out, fmt.Errorf("recovery: file budget exceeded")
			}
			b, e = recoveryReadRegular(full, st)
			if e != nil {
				return out, e
			}
			if st.Mode().Perm()&0111 != 0 {
				mode = "100755"
			}
			if st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return out, fmt.Errorf("recovery: unsupported file permissions")
			}
		} else {
			return out, fmt.Errorf("recovery: unsupported filesystem type")
		}
		total += len(b)
		if total > recoveryMaxBytes {
			return out, fmt.Errorf("recovery: byte budget exceeded")
		}
		if err = recoveryScanContent(b); err != nil {
			return out, err
		}
		if err = recoveryScanPath([]byte(p)); err != nil {
			return out, err
		}
		oid, e := recoveryGitText(ctx, path, nil, b, "hash-object", "-w", "--no-filters", "--stdin")
		if e != nil {
			return out, e
		}
		out.Entries = append(out.Entries, RecoveryEntry{base64.StdEncoding.EncodeToString([]byte(p)), mode, oid})
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Path < out.Entries[j].Path })
	out.Disk, err = recoveryBuildTree(ctx, path, filepath.Join(tmp, "disk-index"), out.Entries)
	if err != nil {
		return out, err
	}
	return out, nil
}

// recoveryReadRegular binds the bytes to the enumerated inode. The kernel
// rejects symlinks in EVERY component and opens nonblocking before fstat, so a
// replaced FIFO cannot hang capture. Never fall back on unsupported platforms.
func recoveryReadRegular(path string, expected os.FileInfo) ([]byte, error) {
	f, err := activityOpenRegular(path)
	if err != nil {
		return nil, fmt.Errorf("recovery: unsafe or unreadable regular file")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !os.SameFile(expected, before) || before.Size() != expected.Size() || before.Mode() != expected.Mode() || before.Size() > recoveryMaxObject {
		return nil, fmt.Errorf("recovery: file changed before read or exceeds budget")
	}
	b, err := io.ReadAll(io.LimitReader(f, recoveryMaxObject+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(b) > recoveryMaxObject || int64(len(b)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || after.ModTime() != before.ModTime() || activityFileMetadata(after) != activityFileMetadata(before) {
		return nil, fmt.Errorf("recovery: file changed during read or exceeds budget")
	}
	return b, nil
}

func recoveryBuildTree(ctx context.Context, path, index string, entries []RecoveryEntry) (string, error) {
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := recoveryCaptureGit(ctx, path, env, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	var data bytes.Buffer
	for _, e := range entries {
		p, err := base64.StdEncoding.DecodeString(e.Path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&data, "%s %s\t", e.Mode, e.OID)
		data.Write(p)
		data.WriteByte(0)
	}
	if _, err := recoveryCaptureGit(ctx, path, env, data.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	return recoveryGitText(ctx, path, env, nil, "write-tree")
}

func recoveryDigest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// RecoveryInventoryDigest re-inventories HEAD, stage-0 index, exact raw disk,
// nonignored membership and all required history. Unknown content is an error,
// never a digest of an incomplete subset. Nested checkouts currently hold.
func RecoveryInventoryDigest(ctx context.Context, checkoutPath string) (string, error) {
	tmp, err := os.MkdirTemp("", "reliant-recovery-inventory-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	inv, err := recoveryInventoryAt(ctx, checkoutPath, tmp)
	if err != nil {
		return "", err
	}
	return recoveryDigest(inv), nil
}
