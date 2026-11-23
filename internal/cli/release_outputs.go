package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// Declared build outputs (kcl/schema.k Build.outputs).
//
// A build's image is recorded by the digest it pushed; the FILES a build
// writes — a CLI archive, an installer, a desktop bundle — are recorded here,
// by hash, from what the build left on disk. The rule is the one the rest of
// the ledger holds to (release_artifacts.go): the release is a projection of
// what the build produced, never a parallel capture. Each declared output is
// matched against the build's working directory and every file it selects
// becomes one release.KindFile artifact.
//
// Unlike the best-effort harvesters beside it, a declared output that matches
// nothing is NOT silently absent: the author said the release ships it, so a
// `--release` build refuses (a release records only bytes that exist) and a
// plain build warns.

// declaredOutputKey is the ledger key of one file a workload's declared
// outputs matched: `<workload>/<path relative to the build's working dir>`.
// The workload prefix keeps two workloads that write the same relative path
// apart, and the slash keeps it apart from a build-only variant's binary,
// which is keyed by its bare output name.
func declaredOutputKey(workload, rel string) string {
	return workload + "/" + rel
}

// harvestDeclaredOutputs hashes every file the env's declared build outputs
// match, as release file artifacts keyed by declaredOutputKey. missing lists
// each output that matched no file, as "<workload>: <path> (in <dir>)"; err is
// a declaration forge cannot evaluate (a bad pattern, an unreadable file).
//
// Version is the file's path relative to the build's working directory: how
// a consumer asks for it within the release, and stable from one release to
// the next, so comparing two releases reports a file only when its bytes
// changed.
func harvestDeclaredOutputs(ctx context.Context, projectDir string, entities *KCLEntities) (out map[string]release.Artifact, missing []string, err error) {
	out = map[string]release.Artifact{}
	if entities == nil {
		return out, nil, nil
	}
	for _, w := range entities.Workloads {
		if len(w.Build.Outputs) == 0 {
			continue
		}
		dir, err := buildOutputsDir(ctx, projectDir, w)
		if err != nil {
			return nil, nil, fmt.Errorf("workload %s: outputs: %w", w.Name, err)
		}
		for _, o := range w.Build.Outputs {
			files, err := buildtarget.MatchOutputs(dir, o.Path)
			if err != nil {
				return nil, nil, fmt.Errorf("workload %s: %w", w.Name, err)
			}
			if len(files) == 0 {
				missing = append(missing, fmt.Sprintf("%s: %s (in %s)", w.Name, o.Path, dir))
				continue
			}
			for _, rel := range files {
				sum, size, err := sha256FileSize(filepath.Join(dir, filepath.FromSlash(rel)))
				if err != nil {
					return nil, nil, fmt.Errorf("workload %s: output %s: %w", w.Name, rel, err)
				}
				art := release.Artifact{
					Kind:      release.KindFile,
					Mode:      release.ModeShared,
					Version:   rel,
					Integrity: sum,
					Size:      size,
				}
				if o.Platform != "" {
					art.Platforms = []string{o.Platform}
				}
				out[declaredOutputKey(w.Name, rel)] = art
			}
		}
	}
	sort.Strings(missing)
	return out, missing, nil
}

// buildOutputsDir is the directory a workload's declared outputs are relative
// to: the working directory its ShellBuild ran in (its resolved `source`
// checkout, else `cwd` against the project root), else the project root —
// where `forge build` runs a GoBuild or DockerBuild.
func buildOutputsDir(ctx context.Context, projectDir string, w WorkloadEntity) (string, error) {
	sh := w.shell()
	if sh == nil {
		return projectDir, nil
	}
	if sh.Source != nil {
		// A --release --no-build cut never ran the build that resolves the
		// checkout; resolve it the same way (a cached, pinned checkout).
		if err := resolveShellBuildSource(ctx, projectDir, sh); err != nil {
			return "", err
		}
		// Use the checkout itself; a missing resolution must never fall
		// back to the project directory and hash a same-named stale file.
		if sh.SourceDir == "" {
			return "", fmt.Errorf("ShellBuild.source did not resolve a checkout directory")
		}
		return resolveProjectPath(projectDir, sh.SourceDir), nil
	}
	return resolveProjectPath(projectDir, sh.Cwd), nil
}

// errMissingDeclaredOutputs is a release's refusal of declared outputs that
// matched nothing.
func errMissingDeclaredOutputs(version string, missing []string) error {
	return fmt.Errorf("--release %s: a declared build output matched no file, and a release records only bytes that exist:\n    %s\n"+
		"  Run the build that writes them (drop --no-build or --target), or fix the path in the build's `outputs`",
		version, strings.Join(missing, "\n    "))
}

// warnMissingDeclaredOutputs is a plain build's version of the release
// refusal: the build succeeded, so a declared output that matched nothing is
// reported, not fatal — the next `--release` build is where it would refuse.
func warnMissingDeclaredOutputs(ctx context.Context, w io.Writer, projectDir string, entities *KCLEntities) {
	_, missing, err := harvestDeclaredOutputs(ctx, projectDir, entities)
	if err != nil {
		fmt.Fprintf(w, "[build]   Warning: could not check the declared build outputs: %v\n", err)
		return
	}
	for _, m := range missing {
		fmt.Fprintf(w, "[build]   Warning: declared output matched no file — %s; a --release build refuses this\n", m)
	}
}

// sha256FileSize streams a file and returns "sha256:<hex>" over its bytes and
// its length. Streamed, not read whole: an installer or a desktop bundle can
// be hundreds of megabytes.
func sha256FileSize(path string) (string, int64, error) {
	f, err := os.Open(path) //nolint:gosec // path is a file the project's own build outputs declared
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return "", 0, err
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}
