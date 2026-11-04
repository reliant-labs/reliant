// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const RecoveryFormatVersion = 1

// RecoveryManifest is the canonical v1 manifest.json at the capture root.
// The only other root entries are the index and disk trees. No commit identity
// appears in this encoding, so ManifestDigest has no circular dependency.
type RecoveryManifest struct {
	OmittedDigest   string          `json:"omitted_digest"`
	Version         int             `json:"version"`
	CaptureID       string          `json:"capture_id"`
	WorktreeID      string          `json:"worktree_id"`
	RepoID          string          `json:"repo_id"`
	Rel             string          `json:"rel"`
	HeadOID         string          `json:"head_oid"`
	Branch          string          `json:"branch"`
	IndexTreeOID    string          `json:"index_tree_oid"`
	DiskTreeOID     string          `json:"disk_tree_oid"`
	ObjectFormat    string          `json:"object_format"`
	HistoryTips     []string        `json:"history_tips"`
	UntrackedPaths  []string        `json:"untracked_paths"`
	DiskEntries     []RecoveryEntry `json:"disk_entries"`
	IndexEntries    []RecoveryEntry `json:"index_entries"`
	InventoryDigest string          `json:"inventory_digest"`
	SecretPolicy    string          `json:"secret_policy"`
	CapturedAt      time.Time       `json:"captured_at"`
}

type recoveryBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *recoveryBoundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("recovery: Git output budget exceeded")
	}
	return b.Buffer.Write(p)
}

// A separate runner leaves old manual snapshots unchanged. Never inherit Git
// object/index/config overrides and never permit a lazy fetch, helper or hook.
// Error text deliberately omits command output, paths and user content.
func recoveryCaptureGit(ctx context.Context, path string, extra []string, input []byte, args ...string) ([]byte, error) {
	prefix := []string{"--no-replace-objects", "-c", "protocol.allow=never", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "commit.gpgSign=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	cmd := gitCommand(ctx, append(prefix, args...))
	cmd.Dir = path
	for _, v := range os.Environ() { //nolint:forbidigo // sanitize the inherited child process environment, not application configuration
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C")
	cmd.Env = append(cmd.Env, extra...)
	cmd.Stdin = bytes.NewReader(input)
	out := &recoveryBoundedBuffer{limit: recoveryMaxBytes + 8<<20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("recovery: local Git operation failed: %w", err)
	}
	return out.Bytes(), nil
}
func recoveryGitText(ctx context.Context, path string, env []string, input []byte, args ...string) (string, error) {
	b, e := recoveryCaptureGit(ctx, path, env, input, args...)
	return strings.TrimSpace(string(b)), e
}

func recoveryManifestInventory(m RecoveryManifest) recoveryInventory {
	return recoveryInventory{OmittedDigest: m.OmittedDigest, Head: m.HeadOID, Branch: m.Branch, Format: m.ObjectFormat, Index: m.IndexTreeOID, Disk: m.DiskTreeOID, Tips: m.HistoryTips, Untracked: m.UntrackedPaths, Entries: m.DiskEntries, IndexEntries: m.IndexEntries}
}

func recoveryDescriptorIdentity(c RecoveryCapture) error {
	for _, s := range []string{c.CaptureID, c.WorktreeID, c.RepoID} {
		if len(s) < 1 || len(s) > 128 || strings.ContainsAny(s, "/\\\x00\n\r ") {
			return fmt.Errorf("recovery: invalid capture identity")
		}
	}
	if c.Rel != "." && c.Rel != "" {
		if err := recoveryPath(c.Rel); err != nil {
			return err
		}
	}
	want := "refs/heads/reliant-recovery/" + c.WorktreeID + "/" + c.CaptureID + "/" + c.RepoID
	if c.RecoveryRef != want {
		return fmt.Errorf("recovery: capture ref does not match identity")
	}
	return nil
}

// CaptureRecovery creates and locally pins a self-contained synthetic commit.
// It performs no network operation. The caller journals the returned immutable
// descriptor before publication; failed capture never authorizes removal.
// This does not mutate real HEAD, branch, index or disk. Objects and a dedicated
// refs/reliant/recovery-pins ref are the only writes to the source repository.
func CaptureRecovery(ctx context.Context, checkoutPath string, c RecoveryCapture) (RecoveryCapture, error) {
	if err := recoveryDescriptorIdentity(c); err != nil {
		return RecoveryCapture{}, err
	}
	if _, err := recoveryCaptureGit(ctx, checkoutPath, nil, nil, "check-ref-format", c.RecoveryRef); err != nil {
		return RecoveryCapture{}, err
	}
	tmp, err := os.MkdirTemp("", "reliant-recovery-capture-")
	if err != nil {
		return RecoveryCapture{}, err
	}
	defer os.RemoveAll(tmp)
	inv, err := recoveryInventoryAt(ctx, checkoutPath, tmp)
	if err != nil {
		return RecoveryCapture{}, err
	}
	if c.CapturedAt.IsZero() {
		c.CapturedAt = time.Now().UTC()
	}
	c.CapturedAt = c.CapturedAt.UTC()
	m := RecoveryManifest{OmittedDigest: inv.OmittedDigest, Version: RecoveryFormatVersion, CaptureID: c.CaptureID, WorktreeID: c.WorktreeID, RepoID: c.RepoID, Rel: c.Rel, HeadOID: inv.Head, Branch: inv.Branch, IndexTreeOID: inv.Index, DiskTreeOID: inv.Disk, ObjectFormat: inv.Format, HistoryTips: inv.Tips, UntrackedPaths: inv.Untracked, DiskEntries: inv.Entries, IndexEntries: inv.IndexEntries, InventoryDigest: recoveryDigest(inv), SecretPolicy: RecoverySecretPolicy, CapturedAt: c.CapturedAt}
	if err = recoveryManifestCheck(m); err != nil {
		return RecoveryCapture{}, err
	}
	manifest, _ := json.Marshal(m)
	blob, err := recoveryGitText(ctx, checkoutPath, nil, manifest, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return RecoveryCapture{}, err
	}
	rootText := fmt.Sprintf("040000 tree %s\tdisk\x00040000 tree %s\tindex\x00100644 blob %s\tmanifest.json\x00", inv.Disk, inv.Index, blob)
	tree, err := recoveryGitText(ctx, checkoutPath, nil, []byte(rootText), "mktree", "-z")
	if err != nil {
		return RecoveryCapture{}, err
	}
	args := []string{"commit-tree", tree}
	for _, tip := range inv.Tips {
		args = append(args, "-p", tip)
	}
	env := []string{"GIT_AUTHOR_NAME=Reliant Recovery", "GIT_AUTHOR_EMAIL=recovery@localhost", "GIT_COMMITTER_NAME=Reliant Recovery", "GIT_COMMITTER_EMAIL=recovery@localhost", "GIT_AUTHOR_DATE=" + c.CapturedAt.Format(time.RFC3339), "GIT_COMMITTER_DATE=" + c.CapturedAt.Format(time.RFC3339)}
	commit, err := recoveryGitText(ctx, checkoutPath, env, []byte("Reliant recovery v1 [skip ci]\n"), args...)
	if err != nil {
		return RecoveryCapture{}, err
	}
	c.Version = RecoveryFormatVersion
	c.CommitOID = commit
	c.ManifestDigest = recoveryDigest(m)
	c.HeadOID = inv.Head
	c.IndexTreeOID = inv.Index
	c.DiskTreeOID = inv.Disk
	c.ObjectFormat = inv.Format
	c.InventoryDigest = m.InventoryDigest
	c.SecretPolicy = RecoverySecretPolicy
	c.HistoryTips = inv.Tips
	c.UntrackedPaths = inv.Untracked
	if err = ValidateRecoveryCapture(ctx, checkoutPath, c); err != nil {
		return RecoveryCapture{}, err
	}
	// Re-read exact inventory before pinning; external writers are not frozen by
	// Git plumbing. Finalization must compare again under its own admission gate.
	now, err := RecoveryInventoryDigest(ctx, checkoutPath)
	if err != nil || now != c.InventoryDigest {
		return RecoveryCapture{}, fmt.Errorf("recovery: checkout changed during capture")
	}
	pin := "refs/reliant/recovery-pins/" + c.WorktreeID + "/" + c.CaptureID + "/" + c.RepoID
	prior, e := recoveryGitText(ctx, checkoutPath, nil, nil, "rev-parse", "--verify", pin)
	if e == nil {
		if prior != commit {
			return RecoveryCapture{}, fmt.Errorf("recovery: local capture identity conflict")
		}
	} else if _, err = recoveryCaptureGit(ctx, checkoutPath, nil, nil, "update-ref", pin, commit, strings.Repeat("0", len(commit))); err != nil {
		return RecoveryCapture{}, err
	}
	return c, nil
}

func recoveryManifestCheck(m RecoveryManifest) error {
	// No omission provenance is supported. Reject previously minted manifests
	// that promised only a digest for omitted leaves rather than their bytes.
	if m.OmittedDigest != recoveryDigest([]RecoveryEntry{}) {
		return fmt.Errorf("recovery: unsupported omitted content")
	}
	if m.Version != RecoveryFormatVersion || m.SecretPolicy != RecoverySecretPolicy {
		return fmt.Errorf("recovery: unsupported manifest/policy version")
	}
	for _, oid := range append([]string{m.HeadOID, m.IndexTreeOID, m.DiskTreeOID}, m.HistoryTips...) {
		if !recoveryOID(oid) {
			return fmt.Errorf("recovery: invalid manifest identity")
		}
	}
	if len(m.HistoryTips) == 0 || len(m.HistoryTips) > recoveryMaxTips || !sort.StringsAreSorted(m.HistoryTips) {
		return fmt.Errorf("recovery: invalid manifest history")
	}
	found := false
	for i, t := range m.HistoryTips {
		if i > 0 && t == m.HistoryTips[i-1] {
			return fmt.Errorf("recovery: duplicate history tip")
		}
		if t == m.HeadOID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("recovery: HEAD absent from parents")
	}
	if m.ObjectFormat != "sha1" && m.ObjectFormat != "sha256" {
		return fmt.Errorf("recovery: invalid object format")
	}
	size := 40
	if m.ObjectFormat == "sha256" {
		size = 64
	}
	if len(m.HeadOID) != size {
		return fmt.Errorf("recovery: object format mismatch")
	}
	if err := recoveryDescriptorIdentity(RecoveryCapture{CaptureID: m.CaptureID, WorktreeID: m.WorktreeID, RepoID: m.RepoID, Rel: m.Rel, RecoveryRef: "refs/heads/reliant-recovery/" + m.WorktreeID + "/" + m.CaptureID + "/" + m.RepoID}); err != nil {
		return err
	}
	// IDs are bounded structural metadata, often random UUIDs; scan known
	// sensitive patterns without classifying those expected random IDs as data.
	for _, s := range []string{m.CaptureID, m.WorktreeID, m.RepoID} {
		for _, detector := range recoverySecretPatterns {
			if detector.MatchString(s) {
				return fmt.Errorf("recovery: sensitive manifest identity")
			}
		}
	}
	for _, s := range []string{m.Rel, m.Branch} {
		if err := recoveryScanContent([]byte(s)); err != nil {
			return err
		}
	}
	if m.InventoryDigest != recoveryDigest(recoveryManifestInventory(m)) {
		return fmt.Errorf("recovery: inventory digest mismatch")
	}
	indexPaths := map[string]bool{}
	diskPaths := map[string]bool{}
	for i, entries := range [][]RecoveryEntry{m.IndexEntries, m.DiskEntries} {
		if len(entries) > recoveryMaxFiles {
			return fmt.Errorf("recovery: manifest entry budget")
		}
		seen := map[string]bool{}
		for j, e := range entries {
			p, err := base64.StdEncoding.DecodeString(e.Path)
			if err != nil || base64.StdEncoding.EncodeToString(p) != e.Path {
				return fmt.Errorf("recovery: invalid path encoding")
			}
			if err = recoveryPath(string(p)); err != nil {
				return err
			}
			if err = recoveryScanPath(p); err != nil {
				return err
			}
			if !recoveryOID(e.OID) || len(e.OID) != size || e.Mode != "100644" && e.Mode != "100755" && e.Mode != "120000" {
				return fmt.Errorf("recovery: unsupported manifest entry")
			}
			if j > 0 && entries[j-1].Path >= e.Path || seen[strings.ToLower(string(p))] {
				return fmt.Errorf("recovery: manifest path collision/order")
			}
			seen[strings.ToLower(string(p))] = true
			if i == 0 {
				indexPaths[e.Path] = true
			} else {
				diskPaths[e.Path] = true
			}
		}
	}
	if !sort.StringsAreSorted(m.UntrackedPaths) {
		return fmt.Errorf("recovery: untracked ordering")
	}
	seen := map[string]bool{}
	for _, p := range m.UntrackedPaths {
		if indexPaths[p] || !diskPaths[p] || seen[p] {
			return fmt.Errorf("recovery: invalid untracked membership")
		}
		seen[p] = true
	}
	for p := range diskPaths {
		if !indexPaths[p] && !seen[p] {
			return fmt.Errorf("recovery: unrecorded untracked entry")
		}
	}
	return nil
}

// ReadRecoveryManifest validates canonical encoding, digest and all descriptor
// fields against manifest.json. It works in a bare repository as well as a
// checkout and invokes no checkout filters. Use ValidateRecoveryCapture before
// accepting remote durability; this helper alone does not scan object closure.
func ReadRecoveryManifest(ctx context.Context, repoPath string, c RecoveryCapture) (RecoveryManifest, error) {
	var m RecoveryManifest
	if err := recoveryDescriptorIdentity(c); err != nil {
		return m, err
	}
	if !recoveryOID(c.CommitOID) {
		return m, fmt.Errorf("recovery: invalid commit identity")
	}
	data, err := recoveryCaptureGit(ctx, repoPath, nil, nil, "show", c.CommitOID+":manifest.json")
	if err != nil {
		return m, err
	}
	if len(data) > recoveryMaxObject {
		return m, fmt.Errorf("recovery: manifest budget")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return m, fmt.Errorf("recovery: malformed manifest")
	}
	canonical, _ := json.Marshal(m)
	if !bytes.Equal(data, canonical) {
		return m, fmt.Errorf("recovery: noncanonical manifest")
	}
	if err = recoveryManifestCheck(m); err != nil {
		return m, err
	}
	if c.Version != m.Version || c.CaptureID != m.CaptureID || c.WorktreeID != m.WorktreeID || c.RepoID != m.RepoID || c.Rel != m.Rel || c.ManifestDigest != recoveryDigest(m) || c.HeadOID != m.HeadOID || c.IndexTreeOID != m.IndexTreeOID || c.DiskTreeOID != m.DiskTreeOID || c.ObjectFormat != m.ObjectFormat || c.InventoryDigest != m.InventoryDigest || c.SecretPolicy != m.SecretPolicy || !c.CapturedAt.Equal(m.CapturedAt) || recoveryDigest(c.HistoryTips) != recoveryDigest(m.HistoryTips) || recoveryDigest(c.UntrackedPaths) != recoveryDigest(m.UntrackedPaths) {
		return m, fmt.Errorf("recovery: descriptor/manifest mismatch")
	}
	return m, nil
}

// ValidateRecoveryCapture is a local no-egress gate, valid for fresh bare
// fetches too. Never trust a supplied SecretPolicy assertion: re-scan the full
// actual closure under the pinned policy before publication and after fetch.
func ValidateRecoveryCapture(ctx context.Context, repoPath string, c RecoveryCapture) error {
	m, err := ReadRecoveryManifest(ctx, repoPath, c)
	if err != nil {
		return err
	}
	root, err := recoveryGitText(ctx, repoPath, nil, nil, "ls-tree", "-z", c.CommitOID)
	if err != nil {
		return err
	}
	manifestBlob, err := recoveryGitText(ctx, repoPath, nil, nil, "rev-parse", c.CommitOID+":manifest.json")
	if err != nil {
		return err
	}
	want := fmt.Sprintf("040000 tree %s\tdisk\x00040000 tree %s\tindex\x00100644 blob %s\tmanifest.json\x00", m.DiskTreeOID, m.IndexTreeOID, manifestBlob)
	if root != strings.TrimSpace(want) {
		return fmt.Errorf("recovery: unexpected capture root tree")
	}
	parents, err := recoveryGitText(ctx, repoPath, nil, nil, "rev-list", "--parents", "-n", "1", c.CommitOID)
	if err != nil {
		return err
	}
	p := strings.Fields(parents)
	if len(p) != len(m.HistoryTips)+1 {
		return fmt.Errorf("recovery: capture parent mismatch")
	}
	for i, t := range m.HistoryTips {
		if p[i+1] != t {
			return fmt.Errorf("recovery: capture history mismatch")
		}
	}
	for _, x := range []struct {
		tree    string
		entries []RecoveryEntry
	}{{m.IndexTreeOID, m.IndexEntries}, {m.DiskTreeOID, m.DiskEntries}} {
		raw, e := recoveryCaptureGit(ctx, repoPath, nil, nil, "ls-tree", "-r", "-z", x.tree)
		if e != nil {
			return e
		}
		entries, e := recoveryEntries(raw)
		if e != nil {
			return e
		}
		if recoveryDigest(entries) != recoveryDigest(x.entries) {
			return fmt.Errorf("recovery: tree inventory mismatch")
		}
	}
	if _, err = recoveryCaptureGit(ctx, repoPath, nil, nil, "fsck", "--strict", "--no-reflogs", "--no-dangling", c.CommitOID); err != nil {
		return fmt.Errorf("recovery: incomplete/invalid object closure")
	}
	return recoveryScanClosure(ctx, repoPath, []string{c.CommitOID}, manifestBlob)
}
