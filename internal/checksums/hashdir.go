package checksums

// One certificate file per comment-incapable output.
//
// WHY NOT ONE MANIFEST. A comment-incapable Tier-1 output (JSON, NDJSON)
// cannot carry the embedded forge:hash marker, so its certificate — the body
// hash of forge's last render — has to live beside it. It used to live in ONE
// file, .forge/hashes.json, a sorted map of every such output. That made the
// manifest the one file every branch that re-rendered ANY JSON output edited:
// two branches that each changed one service's alert spec rewrote two
// neighbouring lines of it, and git (and GitHub's server-side merge, which
// runs no merge driver) reported a conflict although the outputs themselves
// merged cleanly. Measured over the week to 2026-10-30, it conflicted in 26
// pull requests across reliant and control-plane, more than any other forge
// file but the aggregates.
//
// So each certificate is its own file, at the output's path mirrored under
// .forge/hashes/ with a .sha256 suffix:
//
//	deploy/observability/spec/alert-service-chat_gen.json
//	.forge/hashes/deploy/observability/spec/alert-service-chat_gen.json.sha256
//
// holding the hash and a newline. A branch that re-renders one output changes
// that output's certificate and nothing else, so two branches conflict here
// only when they both changed the SAME output — when the output itself is a
// file both changed, and the merge driver and `forge generate` settle both.
// This is the embedded marker's property ("the certificate travels with the
// file, and only that file's changes touch it"), kept for formats that cannot
// embed it, without changing a byte of the output: the observability
// dashboards are read by provisioners that forge does not own, and an NDJSON
// line has nowhere to put a comment.
//
// The directory is committed (the project .gitignore re-includes it). Load
// still reads the legacy .forge/hashes.json, and the next Save writes its
// entries out as certificate files and deletes it: one `forge generate`
// migrates a project.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// hashCertSuffix ends every certificate file's name.
const hashCertSuffix = ".sha256"

// HashCertPath is the project-relative certificate file of the
// comment-incapable output relPath.
func HashCertPath(relPath string) string {
	return HashesDir + "/" + slashKey(relPath) + hashCertSuffix
}

// HashCertTarget is the inverse of HashCertPath: the output a certificate
// file at certRel certifies, and whether certRel is one.
func HashCertTarget(certRel string) (string, bool) {
	certRel = slashKey(certRel)
	rest, ok := strings.CutPrefix(certRel, HashesDir+"/")
	if !ok || !strings.HasSuffix(rest, hashCertSuffix) {
		return "", false
	}
	target := strings.TrimSuffix(rest, hashCertSuffix)
	if target == "" || path.Clean(target) != target || strings.HasPrefix(target, "../") {
		return "", false
	}
	return target, true
}

// IsHashCert reports whether relPath is a certificate file.
func IsHashCert(relPath string) bool {
	_, ok := HashCertTarget(relPath)
	return ok
}

// ParseHashCert reads a certificate file's hash.
func ParseHashCert(data []byte) (string, bool) {
	v := string(bytes.TrimSpace(data))
	return v, isHashValue(v)
}

// encodeHashCert renders a certificate file's content.
func encodeHashCert(hash string) []byte { return []byte(hash + "\n") }

// loadHashCerts reads every certificate under root's HashesDir.
func loadHashCerts(root string) (map[string]string, error) {
	out := map[string]string{}
	dir := filepath.Join(root, filepath.FromSlash(HashesDir))
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target, ok := HashCertTarget(rel)
		if !ok {
			return nil // not a certificate: a stray file is nobody's record
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hash, ok := ParseHashCert(data)
		if !ok {
			return fmt.Errorf("parse %s: not a sha256 hash (restore it from git, or delete it and run `forge generate`)", filepath.ToSlash(rel))
		}
		out[target] = hash
		return nil
	})
	return out, err
}

// saveHashCerts makes root's HashesDir hold exactly certs: it writes each
// certificate whose content differs, removes those of outputs no longer
// certified (pruning emptied directories), and deletes the legacy manifest.
// Every write and removal is journaled, so a failed run restores them.
func saveHashCerts(root string, certs map[string]string) error {
	onDisk, err := loadHashCertFiles(root)
	if err != nil {
		return err
	}
	targets := make([]string, 0, len(certs))
	for target := range certs {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		certRel := HashCertPath(target)
		want := encodeHashCert(certs[target])
		if have, ok := onDisk[certRel]; ok && bytes.Equal(have, want) {
			continue
		}
		recordPreWrite(root, certRel)
		full := filepath.Join(root, filepath.FromSlash(certRel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(full, want, 0o644); err != nil {
			return err
		}
	}
	var stale []string
	for certRel := range onDisk {
		if target, _ := HashCertTarget(certRel); certs[target] == "" {
			stale = append(stale, certRel)
		}
	}
	sort.Strings(stale)
	for _, certRel := range stale {
		recordPreWrite(root, certRel)
		full := filepath.Join(root, filepath.FromSlash(certRel))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		pruneEmptyDirs(filepath.Dir(full), filepath.Join(root, filepath.FromSlash(HashesDir)))
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(LegacyHashesFile))); err == nil {
		recordPreWrite(root, LegacyHashesFile)
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(LegacyHashesFile))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if len(certs) > 0 {
		migrateHashesNegation(root)
	}
	return nil
}

// legacyHashesNegation is the .gitignore line that committed the legacy
// manifest; hashesNegation commits the certificate directory instead.
const (
	legacyHashesNegation = "!" + LegacyHashesFile
	hashesNegation       = "!" + HashesDir + "/"
)

// migrateHashesNegation rewrites the project .gitignore's re-include of the
// legacy manifest into one of the certificate directory, so the
// certificates are committed where the manifest was. Every project forge
// scaffolded ignores .forge/* and re-includes its state; without this, the
// certificates would be ignored and every clone would see its JSON outputs
// as uncertified. Only that exact line is touched: .gitignore is the
// user's, and journaled like any other write so a failed run restores it.
func migrateHashesNegation(root string) {
	migrateGitignoreLine(root, legacyHashesNegation, hashesNegation)
}

// migrateGitignoreLine rewrites the project .gitignore's line from to to,
// touching only that exact line (.gitignore is the user's). A file that
// already holds to keeps one copy. Journaled like any other write, so a
// failed run restores it.
func migrateGitignoreLine(root, from, to string) {
	full := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(full)
	if err != nil {
		return
	}
	lines := strings.SplitAfter(string(data), "\n")
	changed, present := false, false
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case to:
			present = true
		case from:
			lines[i] = strings.Replace(line, from, to, 1)
			changed = true
		}
	}
	if !changed {
		return
	}
	out := strings.Join(lines, "")
	if present {
		// Both lines were there: drop the duplicate.
		out = strings.Replace(out, to+"\n", "", 1)
	}
	recordPreWrite(root, ".gitignore")
	_ = atomicfile.WriteFile(full, []byte(out), 0o644)
}

// loadHashCertFiles maps each certificate file under root's HashesDir
// (project-relative) to its bytes.
func loadHashCertFiles(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	dir := filepath.Join(root, filepath.FromSlash(HashesDir))
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if !IsHashCert(rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}

// pruneEmptyDirs removes dir and each emptied parent up to and including
// stop. Removing a non-empty directory fails, which ends the walk.
func pruneEmptyDirs(dir, stop string) {
	for {
		if os.Remove(dir) != nil || dir == stop {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir || !strings.HasPrefix(parent, stop) {
			return
		}
		dir = parent
	}
}

// IsCommittedState reports whether relPath is one of the files under .forge/
// a project commits: a CommittedStateFiles entry, a certificate file or a
// scaffold ledger entry.
func IsCommittedState(relPath string) bool {
	relPath = slashKey(relPath)
	for _, f := range CommittedStateFiles() {
		if relPath == f {
			return true
		}
	}
	if _, ok := ScaffoldEntryTarget(relPath); ok {
		return true
	}
	return IsHashCert(relPath)
}

// CommittedStatePaths lists the committed state files present under root,
// project-relative and sorted: what a caller that sets .forge/ aside as
// scratch must keep.
func CommittedStatePaths(root string) []string {
	var out []string
	for _, f := range CommittedStateFiles() {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil && info.Mode().IsRegular() {
			out = append(out, f)
		}
	}
	certs, _ := loadHashCertFiles(root)
	for certRel := range certs {
		out = append(out, certRel)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	_, _, entries := readScaffoldLedger(abs)
	for entryRel := range entries {
		out = append(out, entryRel)
	}
	sort.Strings(out)
	return out
}
