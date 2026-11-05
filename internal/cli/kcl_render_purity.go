// File: internal/cli/kcl_render_purity.go
//
// `forge generate` must be a pure function of COMMITTED inputs. This guard
// enforces that across the one seam where it silently was not: KCL evaluation.
//
// A KCL render is not obliged to be side-effect-free. KCL's stdlib has
// file.write, and a project can legitimately materialize config as a side
// effect of a render — control-plane generates its shared dev NATS config that
// way, one account per active dev stack, and `forge env up dev` depends on it.
//
// The problem is which command runs the render. `forge generate` renders KCL
// only to READ things (dev gateway listeners for deploy/k3d-ports.yaml, the
// frontend config probe, the CI frontend set). Those are queries. But a query
// still evaluates the whole module, so every file.write in it fires too — and
// generate has no idea it happened, because those writes bypass forge's own
// write chokepoint and therefore appear in no manifest, no checksum, and no
// rollback journal.
//
// Incident (control-plane, cutting v0.1.9). The dev KCL derives its NATS
// accounts from `.forge/blocks.json` — the machine-local, GITIGNORED
// port-block registry — and writes them to `deploy/nats/nats.conf`, which is
// TRACKED. On a machine where someone had once run `forge env up prod`, the
// registry held an extra key, so a plain `forge generate` on a clean checkout
// emitted an extra NATS account into version control:
//
//	CP_prod_: {
//	  jetstream: enabled
//	  users: [ { user: "control-plane-prod-", password: "control-plane-dev-nats-prod-" } ]
//	}
//
// Two developers on the same commit produced different bytes, and CI's Verify
// Generated Code job failed for whoever's local state differed. The tell was
// that `git status` was dirty after `forge generate` on a checkout nobody had
// edited — with a file forge could not explain, because forge had not written
// it.
//
// ── What this guard does ──────────────────────────────────────────────
//
// It brackets each generate-path render with git and treats any tracked file
// that goes from clean to dirty ACROSS the render as a purity violation: forge
// writes nothing through its own chokepoint while KCL is evaluating, so such a
// file can only have come from a file.write in the module.
//
// It then restores that file and reports the defect. Restoring is safe by
// construction and is the point rather than a courtesy: the file is only
// touched when it was verifiably identical to HEAD immediately before the
// render, so putting HEAD's bytes back cannot discard anyone's work — this
// checkout is routinely shared with other agents — and it leaves `forge
// generate` unable to dirty a clean tree.
//
// The render's VALUE is kept. The write is what gets undone, not the query
// that needed the render, so k3d-ports.yaml and the frontend config still
// generate from the same evaluation.
//
// Scope is deliberately generate-only. `forge env up` / `forge env deploy` arm
// the dev-stack allocator and WANT these writes — materializing per-stack
// config is their job. Only generate claims reproducibility, so only generate
// enforces it.

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// dirtyTrackedFiles returns the project-relative paths of TRACKED files git
// currently reports as modified. Untracked files are excluded on purpose:
// this guard is about reproducibility of what is under version control, and a
// render that drops a new gitignored file into .forge/ is ordinary.
//
// A non-repo (or any git failure) yields nil, which disarms the guard rather
// than failing the run — forge must stay usable outside a git checkout, and
// this is a diagnostic, not a gate.
func dirtyTrackedFiles(projectDir string) map[string]bool {
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=no")
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	dirty := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		// Porcelain v1: "XY <path>", where XY is the two-column status.
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		// A rename reads "R  old -> new"; the post-rename path is what a
		// later comparison would see on disk.
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+len(" -> "):]
		}
		dirty[strings.Trim(path, `"`)] = true
	}
	return dirty
}

// restoreTrackedFileFromHEAD rewrites projectDir/rel with its committed
// content. Only ever called for a file this guard just proved was clean, so
// HEAD's bytes are the bytes that were on disk moments earlier.
//
// It streams the blob out of git rather than shelling `git checkout` /
// `git restore`: those take a pathspec and can be coaxed into touching more
// than the one file, and forge has no business running a mutating git
// porcelain command inside someone's checkout.
func restoreTrackedFileFromHEAD(projectDir, rel string) error {
	cmd := exec.Command("git", "show", "HEAD:"+rel)
	cmd.Dir = projectDir
	blob, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read committed content of %s: %w", rel, err)
	}
	abs := filepath.Join(projectDir, filepath.FromSlash(rel))
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(abs); statErr == nil {
		mode = info.Mode().Perm()
	}
	return atomicfile.WriteFile(abs, blob, mode)
}

// renderKCLPure is RenderKCL for the generate path: it runs the render, then
// undoes any tracked-file write the render performed and returns the paths it
// had to restore. The entities are returned regardless — the query is
// legitimate; only the side effect is not.
//
// Inside a generate run a render whose inputs are unchanged is answered from
// the run's memo (generate_kcl_memo.go), with nothing restored: the render that
// produced it already restored and reported. The entities are shared between
// callers, which only read them. A failed render is not memoized, so a caller
// after it evaluates again, as before.
func renderKCLPure(ctx context.Context, projectDir, env string) (*KCLEntities, []string, error) {
	key, memo := kclRenderMemoKey(projectDir, env)
	if memo {
		if e, hit := kclRenderMemoGet(key); hit {
			return e.entities, nil, nil
		}
	}
	before := dirtyTrackedFiles(projectDir)

	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, nil, err
	}
	restored := restoreImpureKCLWrites(projectDir, before)
	if memo {
		kclRenderMemoPut(key, kclRenderMemoEntry{entities: entities})
	}
	return entities, restored, nil
}

// envRender is one environment's result from renderEnvsPure.
type envRender struct {
	env      string
	entities *KCLEntities
	err      error
}

// renderEnvsParallelism bounds how many env evaluations renderEnvsPure runs at once.
// Each is a `forge kcl eval` child of a few CPU-seconds; a project has a
// handful of envs.
const renderEnvsParallelism = 4

// renderEnvsPure is renderKCLPure for several envs, in the given order: the
// shape of every generate step that walks all of them. The ones the run's memo
// cannot answer are evaluated CONCURRENTLY inside ONE purity bracket — two
// `git status` calls for the batch rather than two per env — and anything an
// evaluation wrote to a tracked file is restored and reported for the batch,
// since concurrent renders cannot say which of them wrote it.
func renderEnvsPure(ctx context.Context, projectDir string, envs []string) []envRender {
	out := make([]envRender, len(envs))
	keys := make([]string, len(envs))
	memo := make([]bool, len(envs))
	var misses []int
	for i, env := range envs {
		out[i].env = env
		keys[i], memo[i] = kclRenderMemoKey(projectDir, env)
		if memo[i] {
			if e, hit := kclRenderMemoGet(keys[i]); hit {
				out[i].entities = e.entities
				continue
			}
		}
		misses = append(misses, i)
	}
	if len(misses) == 0 {
		return out
	}

	before := dirtyTrackedFiles(projectDir)
	sem := make(chan struct{}, renderEnvsParallelism)
	var wg sync.WaitGroup
	for _, i := range misses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i].entities, out[i].err = RenderKCL(ctx, projectDir, envs[i])
		}(i)
	}
	wg.Wait()
	rendered := make([]string, 0, len(misses))
	for _, i := range misses {
		rendered = append(rendered, envs[i])
		if out[i].err != nil {
			out[i].entities = nil
			continue
		}
		if memo[i] {
			kclRenderMemoPut(keys[i], kclRenderMemoEntry{entities: out[i].entities})
		}
	}
	reportImpureRender(strings.Join(rendered, ", "), restoreImpureKCLWrites(projectDir, before))
	return out
}

// restoreImpureKCLWrites is shared by environment renders and structural
// library evaluation on the generate path.
func restoreImpureKCLWrites(projectDir string, before map[string]bool) []string {
	if before == nil {
		return nil
	}
	var restored []string
	for path := range dirtyTrackedFiles(projectDir) {
		if before[path] {
			continue // already dirty going in — someone else's edit, not ours
		}
		if rerr := restoreTrackedFileFromHEAD(projectDir, path); rerr != nil {
			// Reporting the path still beats silence: the user can revert it
			// by hand, and the diagnostic below names why it is dirty.
			fmt.Fprintf(os.Stderr, "⚠️  could not restore %s after an impure KCL render: %v\n", path, rerr)
		}
		restored = append(restored, path)
	}
	sort.Strings(restored)
	return restored
}

// reportImpureRender explains a restored write. The message has to carry the
// diagnosis, because the symptom (a tracked file forge never wrote turning up
// dirty) points at forge rather than at the KCL that actually wrote it.
func reportImpureRender(env string, restored []string) {
	if len(restored) == 0 {
		return
	}
	fmt.Printf("⚠️  The %s KCL render wrote %d tracked file(s); forge reverted them:\n", env, len(restored))
	for _, path := range restored {
		fmt.Printf("   • %s\n", path)
	}
	fmt.Println("    `forge generate` renders KCL only to READ from it, so a file.write in the")
	fmt.Println("    module fires during generate as a side effect. That makes generate's output")
	fmt.Println("    depend on whatever the write derives from — and when that is machine-local")
	fmt.Println("    state (.forge/blocks.json, a port store, anything gitignored), two developers")
	fmt.Println("    on the same commit generate different bytes and CI fails for one of them.")
	fmt.Println("    Move the file.write behind a flag the dev render sets and generate does not,")
	fmt.Println("    or generate the file from committed inputs only. `forge env up` still runs it.")
}
