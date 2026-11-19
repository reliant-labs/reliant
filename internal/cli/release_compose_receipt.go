package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// validateCompositionReceiptSource checks the ACTUAL exported commit, not cwd
// HEAD, against the same go.mod/GitSource pin resolver legacy releases use.
func validateCompositionReceiptSource(ctx context.Context, root string, w WorkloadEntity, r release.BuildReceipt, e *KCLEntities) error {
	if err := r.Validate(); err != nil {
		return err
	}
	cwd, err := buildtarget.ResolveCwd(buildtarget.Spec{ProjectDir: root, BuildCwd: w.EffectiveBuildCwd(), Service: w.Name})
	if err != nil {
		return err
	}
	actual, err := buildtarget.CaptureSource(ctx, cwd, root)
	if err != nil {
		return err
	}
	checker := newReleaseSourceCheck(root, "", "snapshot", true, e)
	seen := map[string]bool{}
	for _, s := range r.Inputs.Sources {
		seen[s.Repo] = true
		// A non-cwd source can still name a required Go module. Module path and
		// canonical repository commonly coincide; do not claim cwd built bytes.
		src := &buildtarget.Source{Repo: s.Repo, Commit: s.Commit}
		if actual != nil && actual.Repo == s.Repo {
			copy := *actual
			src = &copy
			src.Commit = s.Commit
			src.Dirty = false
		}
		if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
			if mod, err := modfile.Parse("go.mod", data, nil); err == nil {
				for _, req := range mod.Require {
					if strings.TrimSuffix(req.Mod.Path, ".git") == strings.TrimSuffix(s.Repo, ".git") {
						src.Module = req.Mod.Path
					}
				}
			}
		}
		pins, err := checker.pinsFor(ctx, src)
		if err != nil {
			return err
		}
		for _, pin := range pins {
			if !pin.admits(s.Commit) {
				return fmt.Errorf("actual exported source %s@%s does not match %s", s.Repo, s.Commit, pin.declared)
			}
		}
	}
	if actual != nil {
		pins, err := checker.pinsFor(ctx, actual)
		if err != nil {
			return err
		}
		if len(pins) > 0 && !seen[actual.Repo] {
			return fmt.Errorf("builder omitted required pinned source %s", actual.Repo)
		}
	}
	return nil
}
