package worktreereclaim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// RestoreRecoveryCheckout builds an independent repository in a NEW directory.
// The caller owns staging/publication of the containing workspace. No checkout,
// smudge/clean filter, hook, original branch, or recovery-ref retirement occurs.
func RestoreRecoveryCheckout(ctx context.Context, bare, destination, branch string, capture RecoveryCapture) error {
	return restoreRecoveryCheckout(ctx, bare, "", destination, branch, capture)
}

// RestoreRecoveryLinkedCheckout registers a new linked worktree against the
// destination project's existing repository. It never checks out or resets an
// existing branch. On failure only this call's new registration is rolled back.
func RestoreRecoveryLinkedCheckout(ctx context.Context, bare, parent, destination, branch string, capture RecoveryCapture) error {
	return restoreRecoveryCheckout(ctx, bare, parent, destination, branch, capture)
}

func restoreRecoveryCheckout(ctx context.Context, bare, parent, destination, branch string, capture RecoveryCapture) (err error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return fmt.Errorf("invalid absolute recovery destination")
	}
	resolved, e := filepath.EvalSymlinks(filepath.Dir(destination))
	if e != nil || resolved != filepath.Dir(destination) {
		return fmt.Errorf("symlink alias in recovery destination")
	}
	if parent != "" {
		if err := validateRecoveryDestinationRepo(ctx, parent); err != nil {
			return err
		}
	}
	if err = ValidateRecoveryCapture(ctx, bare, capture); err != nil {
		return fmt.Errorf("validate recovery: %w", err)
	}
	if _, err = recoveryGit(ctx, bare, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("invalid recovery branch")
	}
	entries, err := recoveryDiskEntries(ctx, bare, capture.DiskTreeOID)
	if err != nil {
		return err
	}
	success := false
	linked := false
	defer func() {
		if !success && linked {
			_ = RollbackInterruptedRecovery(context.WithoutCancel(ctx), parent, destination, branch, capture)
		}
		// Incomplete independent repository administration has no journal
		// proving ownership of every byte. Preserve it on failure.
	}()
	objectRepo := parent
	if parent == "" {
		if err = os.Mkdir(destination, 0700); err != nil {
			return err
		}
		if _, err = recoveryGit(ctx, destination, nil, "init", "--template=", "--object-format="+capture.ObjectFormat, "."); err != nil {
			return err
		}
		objectRepo = destination
	} else {
		// Refuse symlink ancestors and pre-existing destinations. Parent repo must
		// already be installed/registered on this destination machine.
		if _, err = os.Lstat(destination); !os.IsNotExist(err) {
			return fmt.Errorf("recovery destination exists")
		}
	}
	// Fetch only from C's freshly verified isolated store. This copies closure,
	// never alternates. No remote branch is changed or removed by restoration.
	if _, err = recoveryGit(ctx, objectRepo, nil, "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-write-fetch-head", "--", bare, capture.CommitOID); err != nil {
		return err
	}
	if parent != "" {
		pin := "refs/reliant/recovery/" + capture.CaptureID + "/" + capture.RepoID
		if _, err = recoveryGit(ctx, parent, nil, "update-ref", pin, capture.CommitOID, strings.Repeat("0", len(capture.CommitOID))); err != nil {
			existing, e := recoveryGit(ctx, parent, nil, "rev-parse", "--verify", pin)
			if e != nil || strings.TrimSpace(string(existing)) != capture.CommitOID {
				return fmt.Errorf("local recovery pin conflict")
			}
		}
		if _, err = recoveryGit(ctx, parent, nil, "worktree", "add", "--no-checkout", "-b", branch, "--", destination, capture.HeadOID); err != nil {
			return err
		}
		linked = true
	} else {
		if _, err = recoveryGit(ctx, destination, nil, "update-ref", "refs/reliant/recovery/"+capture.CaptureID, capture.CommitOID, strings.Repeat("0", len(capture.CommitOID))); err != nil {
			return err
		}
		if _, err = recoveryGit(ctx, destination, nil, "update-ref", "refs/heads/"+branch, capture.HeadOID, strings.Repeat("0", len(capture.HeadOID))); err != nil {
			return err
		}
		if _, err = recoveryGit(ctx, destination, nil, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
			return err
		}
	}
	if _, err = recoveryGit(ctx, destination, nil, "read-tree", capture.IndexTreeOID); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var total int64
	for _, e := range entries {
		sizeRaw, sizeErr := recoveryGit(ctx, bare, nil, "cat-file", "-s", e.oid)
		size, parseErr := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
		if sizeErr != nil || parseErr != nil || size < 0 || size > 100*1024*1024 || total+size > 512*1024*1024 {
			return fmt.Errorf("recovery disk exceeds materialization budget")
		}
		data, eerr := recoveryGit(ctx, bare, nil, "cat-file", "blob", e.oid)
		if eerr != nil {
			return eerr
		}
		total += int64(len(data))
		if len(data) > 100*1024*1024 || total > 512*1024*1024 {
			return fmt.Errorf("recovery disk exceeds materialization budget")
		}
		if err = root.MkdirAll(filepath.FromSlash(path.Dir(e.name)), 0700); err != nil {
			return err
		}
		if e.mode == "120000" {
			if bytes.IndexByte(data, 0) >= 0 {
				return fmt.Errorf("invalid symlink target")
			}
			// A link is data: never traverse it, including during verification.
			err = root.Symlink(string(data), filepath.FromSlash(e.name))
		} else {
			var f *os.File
			mode := os.FileMode(0644)
			if e.mode == "100755" {
				mode = 0755
			}
			f, err = root.OpenFile(filepath.FromSlash(e.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err == nil {
				_, err = f.Write(data)
				if err == nil {
					err = f.Chmod(mode)
				}
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
		}
		if err != nil {
			return err
		}
		var actual []byte
		if e.mode == "120000" {
			var target string
			target, err = root.Readlink(filepath.FromSlash(e.name))
			actual = []byte(target)
		} else {
			actual, err = root.ReadFile(filepath.FromSlash(e.name))
		}
		if err != nil || !bytes.Equal(actual, data) {
			return fmt.Errorf("restored raw file verification failed")
		}
	}
	tree, err := recoveryGit(ctx, destination, nil, "write-tree")
	if err != nil || strings.TrimSpace(string(tree)) != capture.IndexTreeOID {
		return fmt.Errorf("restored index verification failed")
	}
	head, err := recoveryGit(ctx, destination, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != capture.HeadOID {
		return fmt.Errorf("restored HEAD verification failed")
	}
	if err = ValidateRecoveryCapture(ctx, destination, capture); err != nil {
		return fmt.Errorf("restored closure verification failed: %w", err)
	}
	success = true
	return nil
}

// RepairRecoveryRegistration repairs only the explicitly named moved checkout.
func RepairRecoveryRegistration(ctx context.Context, parent, destination string) error {
	if err := validateRecoveryDestinationRepo(ctx, parent); err != nil {
		return err
	}
	_, err := recoveryGit(ctx, parent, nil, "worktree", "repair", "--", destination)
	return err
}

// RollbackInterruptedRecovery removes only an exact unpublished registration
// recorded in the restore journal. A moved branch or mismatched registration
// holds rather than deleting another actor's workspace.
func RollbackInterruptedRecovery(ctx context.Context, parent, destination, branch string, capture RecoveryCapture) error {
	if err := ValidateInterruptedRecovery(ctx, parent, destination, branch, capture); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		return nil
	}
	if _, err := recoveryGit(ctx, parent, nil, "worktree", "remove", "--force", "--", destination); err != nil {
		return err
	}
	_, err := recoveryGit(ctx, parent, nil, "update-ref", "-d", "refs/heads/"+branch, capture.HeadOID)
	return err
}

// ValidateInterruptedRecovery accepts only a subset of exact captured disk
// entries and either no index yet or the complete captured index. Missing files
// are ordinary interrupted materialization; unknown/changed bytes always hold.
// Callers preflight every repository and its containing staging inventory before
// starting any rollback, not one destructive checkout at a time.
func ValidateInterruptedRecovery(ctx context.Context, parent, destination, branch string, capture RecoveryCapture) error {
	if err := validateRecoveryDestinationRepo(ctx, parent); err != nil {
		return err
	}
	out, err := recoveryGit(ctx, parent, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	found := false
	matches := false
	for _, block := range strings.Split(string(out), "\x00\x00") {
		fields := strings.Split(block, "\x00")
		p, h, b := "", "", ""
		for _, f := range fields {
			if strings.HasPrefix(f, "worktree ") {
				p = strings.TrimPrefix(f, "worktree ")
			}
			if strings.HasPrefix(f, "HEAD ") {
				h = strings.TrimPrefix(f, "HEAD ")
			}
			if strings.HasPrefix(f, "branch ") {
				b = strings.TrimPrefix(f, "branch ")
			}
		}
		if filepath.Clean(p) == filepath.Clean(destination) {
			found = true
			matches = h == capture.HeadOID && b == "refs/heads/"+branch
		}
	}
	if !found {
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			return fmt.Errorf("unregistered interrupted recovery content")
		}
		return nil
	}
	if !matches {
		return fmt.Errorf("interrupted recovery registration changed")
	}
	if _, err := os.Lstat(destination); err != nil {
		return fmt.Errorf("interrupted recovery directory missing or unreadable")
	}
	if err := ValidateRecoveryCapture(ctx, parent, capture); err != nil {
		return err
	}
	// Bind the on-disk .git pointer back to this exact registered clone. A
	// replacement administration file is user/unknown data, not our artifact.
	adminRaw, err := recoveryGit(ctx, destination, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	admin := strings.TrimSpace(string(adminRaw))
	if filepath.Dir(admin) != filepath.Join(parent, ".git", "worktrees") {
		return fmt.Errorf("interrupted recovery administration redirected")
	}
	for p, want := range map[string]string{
		filepath.Join(destination, ".git"): "gitdir: " + admin + "\n",
		filepath.Join(admin, "gitdir"):     filepath.Join(destination, ".git") + "\n",
		filepath.Join(admin, "commondir"):  "../..\n",
	} {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		data, err := recoveryReadRegular(p, info)
		if err != nil || string(data) != want {
			return fmt.Errorf("interrupted recovery administration changed")
		}
	}
	if err := filepath.WalkDir(admin, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(admin, p)
		if err != nil {
			return err
		}
		// git 2.48+ creates an empty per-worktree refs/ directory at
		// `worktree add` (2.43 does not); anything inside it is still unknown.
		if rel == "." || rel == "logs" || rel == "refs" {
			if d.IsDir() {
				return nil
			}
		} else if rel == "HEAD" || rel == "index" || rel == "gitdir" || rel == "commondir" || rel == filepath.Join("logs", "HEAD") {
			if d.Type().IsRegular() {
				return nil
			}
		}
		return fmt.Errorf("interrupted recovery administration has unknown state")
	}); err != nil {
		return err
	}
	index, err := recoveryGit(ctx, destination, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return err
	}
	indexPath := strings.TrimSpace(string(index))
	if info, err := os.Lstat(indexPath); err == nil {
		raw, err := recoveryReadRegular(indexPath, info)
		hashBytes := 20
		if capture.ObjectFormat == "sha256" {
			hashBytes = 32
		}
		if err != nil || recoveryCheckIndex(raw, hashBytes) != nil {
			return fmt.Errorf("interrupted recovery index has unknown state")
		}
		tree, err := recoveryGit(ctx, destination, nil, "write-tree")
		if err != nil || strings.TrimSpace(string(tree)) != capture.IndexTreeOID {
			return fmt.Errorf("interrupted recovery index changed")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("interrupted recovery index unreadable")
	}
	return validateRecoveryPartialDisk(ctx, parent, destination, capture)
}

func validateRecoveryPartialDisk(ctx context.Context, objects, checkout string, c RecoveryCapture) error {
	entries, err := recoveryDiskEntries(ctx, objects, c.DiskTreeOID)
	if err != nil {
		return err
	}
	expected := map[string]recoveryDiskEntry{}
	dirs := map[string]bool{}
	for _, e := range entries {
		expected[e.name] = e
		for p := path.Dir(e.name); p != "."; p = path.Dir(p) {
			dirs[p] = true
		}
	}
	return filepath.WalkDir(checkout, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(checkout, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			if !d.IsDir() {
				return fmt.Errorf("interrupted recovery root changed")
			}
			return nil
		}
		if rel == ".git" {
			if !d.Type().IsRegular() {
				return fmt.Errorf("interrupted recovery administration changed")
			}
			return nil
		}
		if d.IsDir() {
			if !dirs[rel] {
				return fmt.Errorf("unknown interrupted recovery directory")
			}
			return nil
		}
		e, ok := expected[rel]
		if !ok {
			return fmt.Errorf("unknown interrupted recovery file")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		var data []byte
		if e.mode == "120000" && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			data = []byte(target)
		} else if e.mode != "120000" && info.Mode().IsRegular() && ((info.Mode()&0111) != 0) == (e.mode == "100755") {
			data, err = recoveryReadRegular(p, info)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("interrupted recovery file mode changed")
		}
		want, err := recoveryGit(ctx, objects, nil, "cat-file", "blob", e.oid)
		if err != nil || !bytes.Equal(data, want) {
			return fmt.Errorf("interrupted recovery bytes changed")
		}
		return nil
	})
}

// ValidateRestoredRecovery checks actual HEAD, index and raw filesystem, not
// merely that the recovery object still exists. Untracked/extra files fail too.
func ValidateRestoredRecovery(ctx context.Context, checkout, branch string, c RecoveryCapture) error {
	if err := ValidateRecoveryCapture(ctx, checkout, c); err != nil {
		return err
	}
	head, err := recoveryGit(ctx, checkout, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != c.HeadOID {
		return fmt.Errorf("restored HEAD changed")
	}
	symbolic, err := recoveryGit(ctx, checkout, nil, "symbolic-ref", "HEAD")
	if err != nil || strings.TrimSpace(string(symbolic)) != "refs/heads/"+branch {
		return fmt.Errorf("restored branch changed")
	}
	tree, err := recoveryGit(ctx, checkout, nil, "write-tree")
	if err != nil || strings.TrimSpace(string(tree)) != c.IndexTreeOID {
		return fmt.Errorf("restored index changed")
	}
	entries, err := recoveryDiskEntries(ctx, checkout, c.DiskTreeOID)
	if err != nil {
		return err
	}
	expected := map[string]recoveryDiskEntry{}
	for _, e := range entries {
		expected[e.name] = e
	}
	err = filepath.WalkDir(checkout, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(checkout, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		entry, ok := expected[rel]
		if !ok {
			return fmt.Errorf("unexpected restored file")
		}
		delete(expected, rel)
		info, e := d.Info()
		if e != nil {
			return e
		}
		var data []byte
		if entry.mode == "120000" {
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("restored symlink changed")
			}
			target, e := os.Readlink(p)
			if e != nil {
				return e
			}
			data = []byte(target)
		} else {
			if !info.Mode().IsRegular() || ((info.Mode()&0111) != 0) != (entry.mode == "100755") {
				return fmt.Errorf("restored file mode changed")
			}
			data, e = recoveryReadRegular(p, info)
			if e != nil {
				return e
			}
		}
		want, e := recoveryGit(ctx, checkout, nil, "cat-file", "blob", entry.oid)
		if e != nil || !bytes.Equal(data, want) {
			return fmt.Errorf("restored raw bytes changed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("restored file missing")
	}
	return nil
}

type recoveryDiskEntry struct{ name, mode, oid string }

func recoveryDiskEntries(ctx context.Context, bare, tree string) ([]recoveryDiskEntry, error) {
	out, err := recoveryGit(ctx, bare, nil, "ls-tree", "-r", "-z", "--full-tree", tree)
	if err != nil {
		return nil, err
	}
	var entries []recoveryDiskEntry
	seen := map[string]bool{}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
			return nil, fmt.Errorf("unsupported recovery disk entry")
		}
		if err := validateRecoveryRelative(name); err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate/case-colliding recovery path")
		}
		seen[key] = true
		entries = append(entries, recoveryDiskEntry{name, fields[0], fields[2]})
		if len(entries) > 100000 {
			return nil, fmt.Errorf("recovery file-count budget exceeded")
		}
	}
	// Reject file/directory collisions before writing ANY bytes. Symlinks may not
	// be ancestors, even when their target would remain inside the destination.
	for _, e := range entries {
		for p := path.Dir(e.name); p != "."; p = path.Dir(p) {
			if seen[strings.ToLower(p)] {
				return nil, fmt.Errorf("recovery file/directory collision")
			}
		}
	}
	return entries, nil
}

func validateRecoveryRelative(name string) error {
	if name == "" || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00:") {
		return fmt.Errorf("unsafe recovery path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.TrimRight(part, " .") != part {
			return fmt.Errorf("unsafe recovery path component")
		}
	}
	return nil
}

// validateRecoveryDestinationRepo keeps the destination's mutable local config
// out of the restore execution model. Unlike capture, a destination must be an
// ordinary installed main clone: linked/redirected admin or worktree config is
// held rather than guessing which filesystem paths belong to it.
func validateRecoveryDestinationRepo(ctx context.Context, parent string) error {
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("destination repository has symlink aliases")
	}
	admin, err := filepath.EvalSymlinks(filepath.Join(parent, ".git"))
	if err != nil || admin != filepath.Join(parent, ".git") {
		return fmt.Errorf("destination administration symlink")
	}
	for _, name := range []string{"objects", "refs", "index", "config"} {
		p := filepath.Join(admin, name)
		if info, e := os.Lstat(p); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination administration symlink")
		}
	}
	info, err := os.Lstat(filepath.Join(parent, ".git"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("destination must be an installed ordinary main clone")
	}
	raw, err := recoveryGit(ctx, parent, nil, "config", "--local", "--no-includes", "--null", "--list")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "\n")
		if !ok {
			return fmt.Errorf("invalid destination configuration")
		}
		key = strings.ToLower(key)
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "user.name", "user.email", "extensions.objectformat":
			continue
		case "core.bare":
			if value == "false" {
				continue
			}
		}
		if strings.HasPrefix(key, "remote.") && (strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".pushurl") || strings.HasSuffix(key, ".fetch")) {
			continue
		}
		if strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")) {
			continue
		}
		return fmt.Errorf("unsupported executable or path-redirecting destination configuration")
	}
	for _, name := range []string{"commondir", "objects/info/alternates", "config.worktree", "index.lock", "shallow", "info/grafts"} {
		if _, err := os.Lstat(filepath.Join(parent, ".git", filepath.FromSlash(name))); !os.IsNotExist(err) {
			return fmt.Errorf("unsupported destination administration")
		}
	}
	common, err := recoveryGit(ctx, parent, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(strings.TrimSpace(string(common))) != filepath.Join(parent, ".git") {
		return fmt.Errorf("destination common directory mismatch")
	}
	index, err := recoveryGit(ctx, parent, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil || filepath.Clean(strings.TrimSpace(string(index))) != filepath.Join(parent, ".git", "index") {
		return fmt.Errorf("destination index path mismatch")
	}
	return nil
}

// recoveryGit uses no ambient git, credential, object, index, template or config
// environment. Commands here operate on already verified local objects only.
func recoveryGit(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "-c", "protocol.allow=never", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"} //nolint:forbidigo // PATH locates Git in an isolated child environment; it is not application configuration
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("recovery git %s failed", strconv.Quote(args[0]))
	}
	return out, nil
}
