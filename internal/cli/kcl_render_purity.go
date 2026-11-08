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
// It then restores that file and reports the defect, so `forge generate`
// cannot dirty a clean tree. Restoring is NOT safe by construction: "forge
// writes nothing while KCL evaluates" says nothing about anyone else, and
// this checkout is routinely shared with other agents, so a file edited by
// hand during the render looks exactly like the render's write. growth lost
// two edited test files that way (restoreImpureKCLWrites has the details).
// So the guard restores only a file the module's KCL names alongside a
// file.write call, and only once a second render proves the dirty bytes are
// the render's own output.
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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	blob, err := committedContent(projectDir, rel)
	if err != nil {
		return err
	}
	return writeKeepingMode(filepath.Join(projectDir, filepath.FromSlash(rel)), blob)
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
	restored := restoreImpureKCLWrites(projectDir, before, func() {
		_, _ = RenderKCL(ctx, projectDir, env)
	})
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
	renderEach := func(store bool) {
		sem := make(chan struct{}, renderEnvsParallelism)
		var wg sync.WaitGroup
		for _, i := range misses {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				entities, err := RenderKCL(ctx, projectDir, envs[i])
				if store {
					out[i].entities, out[i].err = entities, err
				}
			}(i)
		}
		wg.Wait()
	}
	renderEach(true)
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
	reportImpureRender(strings.Join(rendered, ", "), restoreImpureKCLWrites(projectDir, before, func() { renderEach(false) }))
	return out
}

// restoreImpureKCLWrites is shared by environment renders and structural
// library evaluation on the generate path.
//
// A tracked file that went clean → dirty across the render was written by
// the render OR by someone else while it ran: another agent in a shared
// checkout, an editor. The two are indistinguishable from the bytes, and
// restoring the second destroys their work. That happened to growth (friction
// #18): two committed scaffold tests replaced with real tests during a long
// generate came back as the scaffold, restored from HEAD by this guard. So:
//
//   - a module with no file.write cannot have written anything, and every
//     such file is someone else's edit. Nothing is restored.
//   - a module that does write files can only have written a path it names,
//     so a file no string literal in its KCL names is someone else's edit
//     and is left alone, whatever else the module writes.
//   - a path literal is not proof, though: the module may only mention the
//     path. So a named file is reverted only once the render is PROVEN to
//     have written its bytes. Its dirty bytes are kept aside, the file is put
//     back to HEAD and the render runs again (rerender). If the render writes
//     the same bytes again, they are its output and the file stays at HEAD.
//     If the file is still at HEAD, the render does not write it and the
//     dirty bytes were someone's edit: they go back. Anything else is
//     unproven, and the newest bytes stay on disk.
//
// The dirty bytes are copied under .forge/kcl-reverted/ before the file is
// touched, under a name no later run reuses, and the copy is removed once
// their fate is decided, so an edit survives even a crash mid-proof.
func restoreImpureKCLWrites(projectDir string, before map[string]bool, rerender func()) []string {
	if before == nil {
		return nil
	}
	var dirtied []string
	for path := range dirtyTrackedFiles(projectDir) {
		if !before[path] { // already dirty going in — someone else's edit, not ours
			dirtied = append(dirtied, path)
		}
	}
	if len(dirtied) == 0 {
		return nil
	}
	targets := kclFileWriteTargets(projectDir)

	type candidate struct {
		path, stash string
		dirty       []byte
		head        []byte
	}
	var candidates []candidate
	for _, path := range dirtied {
		if !targets[path] {
			continue // the module never names it: someone else's edit
		}
		abs := filepath.Join(projectDir, filepath.FromSlash(path))
		dirty, rerr := os.ReadFile(abs)
		if rerr != nil {
			continue // gone or unreadable: nothing forge could put back either
		}
		head, herr := committedContent(projectDir, path)
		if herr != nil {
			continue
		}
		stash, serr := stashKCLReverted(projectDir, path, dirty)
		if serr != nil {
			// Without a copy, the proof below could be the only record of a
			// concurrent edit lost. Leave the file as it is.
			fmt.Fprintf(os.Stderr, "⚠️  %s changed during a KCL render; left as is (could not keep a copy: %v)\n", path, serr)
			continue
		}
		kclResetHook(abs)
		if werr := replaceIfUnchanged(abs, dirty, head); werr != nil {
			if errors.Is(werr, errChangedUnderUs) {
				// Edited again since it was read: those bytes are newer than
				// anything the proof could decide about, so they stay, and
				// the copy of the older ones is no longer needed.
				_ = os.Remove(stash)
				fmt.Fprintf(os.Stderr, "⚠️  %s changed again during a KCL render; left as is\n", path)
				continue
			}
			fmt.Fprintf(os.Stderr, "⚠️  %s changed during a KCL render; left as is (could not reset it to HEAD: %v)\n", path, werr)
			continue
		}
		candidates = append(candidates, candidate{path: path, stash: stash, dirty: dirty, head: head})
	}
	if len(candidates) == 0 {
		return nil
	}

	rerender()

	var restored []string
	for _, c := range candidates {
		abs := filepath.Join(projectDir, filepath.FromSlash(c.path))
		again, rerr := os.ReadFile(abs)
		switch {
		case rerr == nil && bytes.Equal(again, c.dirty):
			// The render wrote these bytes twice: they are its output.
			if werr := replaceIfUnchanged(abs, again, c.head); werr != nil {
				// Reporting the path still beats silence: the user can revert
				// it by hand, and the diagnostic names why it is dirty.
				fmt.Fprintf(os.Stderr, "⚠️  could not restore %s after an impure KCL render: %v\n", c.path, werr)
			}
			restored = append(restored, c.path)
			_ = os.Remove(c.stash)
		case rerr == nil && bytes.Equal(again, c.head):
			// The render left it at HEAD: the dirty bytes were someone's edit.
			if werr := replaceIfUnchanged(abs, again, c.dirty); werr != nil {
				fmt.Fprintf(os.Stderr, "⚠️  %s was edited during a KCL render and could not be put back (%v); the edit is kept at %s\n", c.path, werr, c.stash)
				continue
			}
			_ = os.Remove(c.stash)
		default:
			// Neither: a render that writes different bytes each time, or an
			// edit landing during the proof. Nothing proves the bytes on disk
			// are the render's, so they stay; the earlier bytes stay kept.
			fmt.Fprintf(os.Stderr, "⚠️  %s changed during a KCL render and forge could not prove the render wrote it; left as is (the earlier bytes are kept at %s)\n", c.path, c.stash)
		}
	}
	sort.Strings(restored)
	return restored
}

// committedContent returns rel's bytes at HEAD.
func committedContent(projectDir, rel string) ([]byte, error) {
	cmd := exec.Command("git", "show", "HEAD:"+rel)
	cmd.Dir = projectDir
	blob, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read committed content of %s: %w", rel, err)
	}
	return blob, nil
}

// errChangedUnderUs is replaceIfUnchanged's answer when the file no longer
// holds the bytes the caller decided about.
var errChangedUnderUs = errors.New("file changed since it was read")

// kclResetHook runs just before restoreImpureKCLWrites resets a candidate to
// HEAD. Tests set it to land an edit in that window; it is a no-op otherwise.
var kclResetHook = func(string) {}

// replaceIfUnchanged writes next to abs only if abs still holds expected,
// re-read immediately before the write. Every write restoreImpureKCLWrites
// makes goes through it, so an edit that lands after the file was read (an
// editor, another agent) is never overwritten by bytes chosen from an older
// read. What remains is the instant between this re-read and the atomic
// rename, which no in-process check can close against an outside writer.
func replaceIfUnchanged(abs string, expected, next []byte) error {
	cur, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, expected) {
		return errChangedUnderUs
	}
	return writeKeepingMode(abs, next)
}

// writeKeepingMode writes data to abs with abs's current permissions.
func writeKeepingMode(abs string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	return atomicfile.WriteFile(abs, data, mode)
}

// kclRevertedDir keeps the bytes of a file restoreImpureKCLWrites is
// deciding about, by project-relative path, so an edit survives a run that
// dies mid-proof, and an unproven one stays recoverable.
const kclRevertedDir = ".forge/kcl-reverted"

// stashKCLReverted writes data to a new file beside kclRevertedDir/rel,
// named so that no later run overwrites it, and returns its path.
func stashKCLReverted(projectDir, rel string, data []byte) (string, error) {
	dst := filepath.Join(projectDir, filepath.FromSlash(kclRevertedDir), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// kclFileWriteRE matches a call to KCL's file.write.
var kclFileWriteRE = regexp.MustCompile(`\bfile\.write\s*\(`)

// kclStringLiteralRE matches a single-line KCL string literal, double- or
// single-quoted.
var kclStringLiteralRE = regexp.MustCompile(`"([^"\\\n]*)"|'([^'\\\n]*)'`)

// kclFileWriteTargets returns the project-relative paths the project's KCL
// could name as a file.write target: every string literal in its sources,
// read as a path both from the project root (file.write resolves a relative
// path against the process's working directory, the project) and from the
// .k file's own directory. It is nil when no source calls file.write, the
// only way a render writes a file. Comment lines do not count. Directories
// that hold no project KCL (VCS, forge state, installed packages, generated
// code) are skipped, and so is a file forge cannot read: a path no literal
// names is never restored, so missing evidence keeps the bytes on disk.
//
// A literal naming the path is not proof the render wrote it (that proof is a
// second render, in restoreImpureKCLWrites), only a precondition: a tracked
// file the module never names cannot be its write, and is someone else's
// edit, so it is never touched at all.
func kclFileWriteTargets(projectDir string) map[string]bool {
	writes := false
	targets := map[string]bool{}
	_ = filepath.WalkDir(projectDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".forge", "node_modules", "vendor", "gen":
				if p != projectDir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(p) != ".k" {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		relDir, rerr := filepath.Rel(projectDir, filepath.Dir(p))
		if rerr != nil {
			relDir = "."
		}
		relDir = filepath.ToSlash(relDir)
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") {
				continue
			}
			if kclFileWriteRE.MatchString(t) {
				writes = true
			}
			for _, m := range kclStringLiteralRE.FindAllStringSubmatch(t, -1) {
				lit := m[1] + m[2]
				if lit == "" || path.IsAbs(lit) {
					continue
				}
				targets[path.Clean(lit)] = true
				targets[path.Join(relDir, lit)] = true
			}
		}
		return nil
	})
	if !writes {
		return nil
	}
	return targets
}

// reportImpureRender explains a restored write. The message has to carry the
// diagnosis, because the symptom (a tracked file forge never wrote turning up
// dirty) points at forge rather than at the KCL that actually wrote it.
//
// On stderr: the renders this guards also run under commands whose stdout a
// script reads (`forge ci gate --list` discovers frontends through them), and
// a warning there is not output (forge report 2186e95b).
func reportImpureRender(env string, restored []string) {
	if len(restored) == 0 {
		return
	}
	w := os.Stderr
	fmt.Fprintf(w, "⚠️  The %s KCL render wrote %d tracked file(s); forge reverted them:\n", env, len(restored))
	for _, path := range restored {
		fmt.Fprintf(w, "   • %s\n", path)
	}
	fmt.Fprintln(w, "    `forge generate` renders KCL only to READ from it, so a file.write in the")
	fmt.Fprintln(w, "    module fires during generate as a side effect. That makes generate's output")
	fmt.Fprintln(w, "    depend on whatever the write derives from — and when that is machine-local")
	fmt.Fprintln(w, "    state (.forge/blocks.json, a port store, anything gitignored), two developers")
	fmt.Fprintln(w, "    on the same commit generate different bytes and CI fails for one of them.")
	fmt.Fprintln(w, "    Move the file.write behind a flag the dev render sets and generate does not,")
	fmt.Fprintln(w, "    or generate the file from committed inputs only. `forge env up` still runs it.")
	fmt.Fprintln(w, "    Each was reverted only after a second render wrote the same bytes again.")
}
