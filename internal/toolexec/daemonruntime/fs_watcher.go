package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/filetree"
	"github.com/reliant-labs/reliant/internal/logging"
)

const fileTreePollInterval = 5 * time.Second

// runFileTreeWatcher polls the filesystem and sends a FileSystemChanged
// message when the file tree hash changes. Sleeps between polls (not
// fixed interval) so a slow poll doesn't cause overlap.
func (d *daemonClient) runFileTreeWatcher(ctx context.Context, projectPath string) {
	var lastHash string

	for {
		hash, err := hashFileTree(ctx, projectPath)
		if err != nil {
			logging.Debug(logPrefix+" File tree hash error", "projectPath", projectPath, "error", err)
		} else {
			if lastHash != "" && hash != lastHash {
				msg := &reliantv1.DaemonMessage{
					Message: &reliantv1.DaemonMessage_FileSystemChanged{
						FileSystemChanged: &reliantv1.FileSystemChanged{
							ProjectPath:     projectPath,
							TimestampUnixMs: time.Now().UTC().UnixMilli(),
						},
					},
				}
				if err := d.send(msg); err != nil {
					logging.Warn(logPrefix+" Failed to send file system changed", "projectPath", projectPath, "error", err)
				}
			}
			lastHash = hash
		}

		// Sleep BETWEEN polls — if the poll took 3s, next one starts 8s after the previous
		select {
		case <-ctx.Done():
			return
		case <-time.After(fileTreePollInterval):
		}
	}
}

// hashFileTree returns a hash representing the current state of the file tree.
// For git repos, uses git commands (fast, naturally respects .gitignore).
// For non-git repos, walks the filesystem.
func hashFileTree(ctx context.Context, projectPath string) (string, error) {
	if isGitRepo(projectPath) {
		return hashGitFileTree(ctx, projectPath)
	}
	return hashWalkDir(ctx, projectPath)
}

// hashGitFileTree hashes the output of git ls-files + git status to detect
// any file additions, deletions, modifications, or untracked files.
// Typically completes in ~30-50ms.
func hashGitFileTree(ctx context.Context, projectPath string) (string, error) {
	h := sha256.New()

	// git ls-files: all tracked files (detects additions/deletions/renames)
	lsCmd := exec.CommandContext(ctx, "git", "ls-files")
	lsCmd.Dir = projectPath
	lsOutput, err := lsCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}
	h.Write(lsOutput)
	h.Write([]byte("\n---\n"))

	// git status --porcelain: modified/staged/untracked (detects content changes)
	stCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	stCmd.Dir = projectPath
	stOutput, err := stCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	h.Write(stOutput)

	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashWalkDir walks the directory tree, hashing path+modtime+size for each
// entry. It runs on the poll timer for projects that are NOT git repositories,
// so it has to stay cheap on every pass: it skips the canonical noise
// directories and stops at the shared node budget.
//
// Without that budget this was a fully unbounded walk of an arbitrary tree
// repeated every five seconds — the same shape of defect as the file-tree RPC,
// just quieter. A truncated walk still produces a stable, sensitive hash: it
// covers a deterministic prefix of the tree, so edits within that prefix are
// still detected. The truncation is logged once per poll so a project that
// outgrew the budget is visible rather than silently half-watched.
//
// Yields every 1000 entries to avoid hogging the goroutine.
func hashWalkDir(ctx context.Context, projectPath string) (string, error) {
	h := sha256.New()
	count := 0

	truncated, err := filetree.WalkHashable(projectPath, filetree.MaxTreeNodes, func(path string, d fs.DirEntry) error {
		// Yield periodically to avoid hogging the goroutine
		count++
		if count%1000 == 0 {
			runtime.Gosched()
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		rel, relErr := filepath.Rel(projectPath, path)
		if relErr != nil {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		fmt.Fprintf(h, "%s\t%d\t%d\n", rel, info.ModTime().UnixNano(), info.Size())
		return nil
	})
	if err != nil {
		return "", err
	}
	if truncated {
		logging.Debug(logPrefix+" File tree hash truncated by node budget",
			"projectPath", projectPath, "maxNodes", filetree.MaxTreeNodes)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
