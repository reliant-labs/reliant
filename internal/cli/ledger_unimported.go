package cli

// The checkout still holds a ledger the selected one has never imported.
//
// WHY THIS REFUSES RATHER THAN FALLING BACK. forge used to keep promotions in
// .forge/promotions/<env>.jsonl and releases in .forge/releases/*.json,
// INSIDE the checkout. Those files are no longer read: the ledger is either
// the control plane (an env that declares forge.ControlPlane) or the
// machine-scoped store under $FORGE_LEDGER_HOME. control-plane's own
// checkout holds 10 promotions and 39 releases in the retired location.
//
// If a command simply read the new, empty ledger, `forge env deploy prod`
// would find NO promotion for prod, conclude "never promoted", and fall back
// to deploying mutable tags instead of the digests those promotions froze.
// That is the single failure this entire design exists to prevent, and it
// would happen silently, on the most important env, at the moment of a
// release. So a checkout ledger the selected store has not imported is a
// hard refusal naming the remedy — the same fail-loud shape as the retired
// errLegacyLedger it replaces.
//
// IT APPLIES TO BOTH BACKENDS. The doc states the refusal for an env whose
// ledger moved to a control plane (§11.3) and for a non-empty machine ledger
// (§12.4), but the hazard is identical whenever the SELECTED ledger lacks
// records the checkout holds — whether that is a control plane or the machine
// ledger of an env that declares none. A rule that fired only for one backend
// would miss exactly the case that motivated it. (control-plane's prod now
// declares forge.ControlPlane, so it lands on the hosted ledger.)
//
// THE ONE BYPASS is `forge env deploy <env> --offline-ledger`, which deploys
// from a cached copy of the hosted ledger because the control plane is down
// (offline_ledger.go). It selects the offline ledger in ledgerFor, which skips
// this check for that selection alone; no variable or other flag does.
//
// IT GOES QUIET BY CONSTRUCTION. Once `forge ledger import` has recorded
// those promotions, the selected ledger contains them, the comparison finds
// nothing missing, and the refusal stops — with no flag to remember and no
// state beyond the ledger itself.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// retiredPromotionsDirRel and retiredReleasesDirRel are the IN-CHECKOUT
// locations forge no longer uses. They are never read AS a ledger — there is
// no dual-read — but they are detected, so a project that has not imported
// fails loudly instead of reading as "never promoted".
const (
	retiredPromotionsDirRel = ".forge/promotions"
	retiredReleasesDirRel   = ".forge/releases"
)

// errLedgerNotImported is a refusal no retry can fix: the import must run.
// A sentinel so a caller classifies it with errors.Is rather than by text.
var errLedgerNotImported = errors.New("checkout holds a ledger that was never imported")

// unimportedLedger counts what one env's checkout holds that the selected
// ledger does not.
type unimportedLedger struct {
	Promotions int
	Releases   int
}

// any reports whether there is anything to refuse over.
func (u unimportedLedger) any() bool { return u.Promotions > 0 || u.Releases > 0 }

// ledgerGitReadTimeout bounds the `git show` reads. A wedged git must not
// hang a command whose real work is elsewhere.
const ledgerGitReadTimeout = 10 * time.Second

// checkLedgerImported refuses when env's checkout holds promotions or
// releases the selected ledger has never imported.
//
// It reads the checkout AT HEAD rather than from the working tree, because
// the ledger was committed: a working tree that has had .forge/promotions
// deleted but not committed still has the records in its history, and the
// authority is what the repository holds. A project that is not a git
// checkout falls back to the working tree, which is all it has.
// COST. Selection runs on every command, and `forge env status` with no
// argument runs it once per environment, so the common case has to be
// cheap: hasRetiredLedger answers from an os.Stat for a checkout that has
// the directory, and its git fallback is memoized per project, so a
// twelve-env status costs at most one `git ls-tree`. Everything after that
// — reading the lines, querying the selected ledger — happens only for a
// project that genuinely has unimported history.
func checkLedgerImported(ctx context.Context, projectDir, env string, l envLedger) error {
	if !hasRetiredLedger(ctx, projectDir) {
		return nil
	}
	held, err := checkoutLedgerFor(ctx, projectDir, env)
	if err != nil || !held.any() {
		return err
	}
	return checkCheckoutLedgerImported(ctx, held, projectDir, env, l)
}

// checkCheckoutLedgerImported is the remote containment half. Callers with a
// network deadline can read the checkout first without spending that budget.
func checkCheckoutLedgerImported(ctx context.Context, held checkoutLedger, projectDir, env string, l envLedger) error {
	missing, err := unimportedIn(ctx, held, projectDir, env, l)
	if err != nil || !missing.any() {
		return err
	}
	return fmt.Errorf("%w: the checkout holds %d promotions (and %d releases) for %s that this ledger has never imported. "+
		"Run `forge ledger import --from-git` (it reads them at a git rev and records them in %s)",
		errLedgerNotImported, missing.Promotions, missing.Releases, env, l.Bindings.Location())
}

// retiredLedgerPresence memoizes "does this project have a retired ledger at
// all", per project directory, for the lifetime of the process.
//
// Memoized because the answer cannot change under a running command — a
// forge invocation does not rewrite its own checkout's history — and because
// without it a no-argument `forge env status` would shell out to git once per
// environment to learn the same fact.
var retiredLedgerPresence sync.Map // projectDir -> bool

// hasRetiredLedger reports whether the checkout carries the retired
// in-checkout ledger, in the working tree OR at HEAD.
//
// HEAD matters and is not an edge case: the retired ledger was COMMITTED, so
// `rm -rf .forge/promotions` without a commit leaves the history — and the
// hazard — fully intact. A check that looked only at the working tree would
// let that delete silence the refusal while a versionless deploy still had
// no promotions to read.
func hasRetiredLedger(ctx context.Context, projectDir string) bool {
	if cached, ok := retiredLedgerPresence.Load(projectDir); ok {
		return cached.(bool)
	}
	present := false
	for _, dir := range []string{retiredPromotionsDirRel, retiredReleasesDirRel} {
		if _, err := os.Stat(filepath.Join(projectDir, dir)); err == nil {
			present = true
			break
		}
	}
	if !present {
		// Nothing in the working tree. Ask git ONCE whether HEAD has it.
		if prefix, isGit := gitPrefix(ctx, projectDir); isGit {
			for _, dir := range []string{retiredPromotionsDirRel, retiredReleasesDirRel} {
				if _, err := gitShow(ctx, projectDir, "HEAD:"+prefix+dir); err == nil {
					present = true
					break
				}
			}
		}
	}
	retiredLedgerPresence.Store(projectDir, present)
	return present
}

// checkoutLedger is one env's slice of the retired ledger as the checkout
// holds it — its promotions and the releases they bind: the actual records,
// so containment can be judged rather than guessed from counts.
type checkoutLedger struct {
	Promotions []release.Promotion
	Releases   []release.Release
}

func (c checkoutLedger) any() bool { return len(c.Promotions) > 0 || len(c.Releases) > 0 }

// checkoutLedgerFor reads the retired in-checkout ledger for one env: its
// promotions, and the releases THOSE promotions bind.
//
// SCOPED TO THE ENV, RELEASES INCLUDED. The hazard is one env reading as
// "never promoted" when the checkout says otherwise, so the history at stake
// is that env's promotions and the releases they froze — nothing else. A
// release no promotion of env references is not env's history: an env the
// checkout never promoted loses nothing by reading an empty ledger. Counting
// the checkout's releases project-wide made exactly those envs refuse —
// control-plane's dev-k8s and e2e, deployed from CI runners whose machine
// ledger starts empty on every run, where no import could ever clear it.
// The import still brings every release (it is project-wide by design); this
// is only what one env's refusal is about.
func checkoutLedgerFor(ctx context.Context, projectDir, env string) (checkoutLedger, error) {
	var out checkoutLedger
	promotions, err := readCheckoutFile(ctx, projectDir,
		filepath.ToSlash(filepath.Join(retiredPromotionsDirRel, releaseFileStem(env)+".jsonl")))
	if err != nil {
		return out, err
	}
	for _, line := range ledgerLines(string(promotions)) {
		var p release.Promotion
		// A line that does not decode is SKIPPED here, unlike in the
		// ledger proper. This is a detector for history that has to be
		// imported, and refusing to run at all because one archived
		// line is malformed would wedge the project on a file nothing
		// reads any more — the import is where those lines are
		// validated.
		if json.Unmarshal([]byte(line), &p) == nil && p.Env == env {
			out.Promotions = append(out.Promotions, p)
		}
	}
	if len(out.Promotions) == 0 {
		// Never promoted here: there are no bound releases to read, and
		// skipping the read keeps the common case to one file lookup.
		return out, nil
	}
	bound := make(map[string]struct{}, len(out.Promotions))
	for _, p := range out.Promotions {
		bound[p.Release] = struct{}{}
	}
	releases, err := readCheckoutReleases(ctx, projectDir)
	if err != nil {
		return out, err
	}
	for _, r := range releases {
		if _, ok := bound[r.Version]; ok {
			out.Releases = append(out.Releases, r)
		}
	}
	return out, nil
}

// unimportedIn counts the checkout's records the selected ledger lacks.
//
// CONTAINMENT IS JUDGED BY IDENTITY, not by count. A promotion is matched by
// its ID and a release by its version, so a ledger holding an env's whole
// history reports nothing missing, and a PARTIAL import reports only the
// remainder — which is what makes a resumed or re-run import converge
// instead of refusing forever.
//
// THE CONTRACT THIS PLACES ON THE IMPORT (F7). The import must PRESERVE each
// promotion's id from the git ledger, and record the source in the
// imported_from field the control plane already has a column for. If it
// minted fresh ids instead, every imported promotion would look absent here
// and the refusal would never go quiet — so this comparison is what the
// import's "ids are preserved" test exists to protect. The alternative,
// matching on (env, release, promoted_at), was rejected: it would silently
// treat two genuine re-promotes of one version as already-imported.
func unimportedIn(ctx context.Context, held checkoutLedger, projectDir, env string, l envLedger) (unimportedLedger, error) {
	var missing unimportedLedger

	have, err := ledgerPromotionIDs(ctx, env, l)
	if err != nil {
		return missing, err
	}
	for _, p := range held.Promotions {
		if _, ok := have[p.ID]; p.ID != "" && ok {
			continue
		}
		missing.Promotions++
	}

	haveReleases, err := ledgerReleaseVersions(ctx, l)
	if err != nil {
		return missing, err
	}
	for _, r := range held.Releases {
		if _, ok := haveReleases[r.Version]; ok {
			continue
		}
		missing.Releases++
	}
	return missing, nil
}

// ledgerPromotionIDs is the set of promotion identities the selected ledger
// holds for env, including the imported_from markers of records whose id was
// assigned by the importing backend.
func ledgerPromotionIDs(ctx context.Context, env string, l envLedger) (map[string]struct{}, error) {
	have := map[string]struct{}{}
	reader, ok := l.Bindings.(bindingHistoryReader)
	if !ok {
		return have, nil
	}
	page, err := reader.HistoryPage(ctx, env, historyQuery{Limit: maxHistoryLimit})
	if err != nil {
		return nil, fmt.Errorf("read %s's promotions from %s: %w", env, l.Bindings.Location(), err)
	}
	for {
		for _, p := range page.Promotions {
			if p.ID != "" {
				have[p.ID] = struct{}{}
			}
		}
		if page.Next == "" {
			return have, nil
		}
		page, err = reader.HistoryPage(ctx, env, historyQuery{Limit: maxHistoryLimit, Before: page.Next})
		if err != nil {
			return nil, fmt.Errorf("read %s's promotions from %s: %w", env, l.Bindings.Location(), err)
		}
	}
}

// ledgerReleaseVersions is the set of versions the selected ledger holds.
func ledgerReleaseVersions(ctx context.Context, l envLedger) (map[string]struct{}, error) {
	have := map[string]struct{}{}
	if l.Releases == nil {
		return have, nil
	}
	all, err := l.Releases.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read releases from %s: %w", l.Releases.Location(), err)
	}
	for _, r := range all {
		have[r.Version] = struct{}{}
	}
	return have, nil
}

// readCheckoutFile reads one repo-relative path at HEAD, falling back to the
// working tree when the project is not a git checkout (or the file is
// untracked, which is the state between writing a ledger and committing it).
func readCheckoutFile(ctx context.Context, projectDir, relPath string) ([]byte, error) {
	if prefix, ok := gitPrefix(ctx, projectDir); ok {
		if out, err := gitShow(ctx, projectDir, "HEAD:"+prefix+relPath); err == nil {
			return out, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(projectDir, relPath)) //nolint:gosec // relPath is a constant dir plus a sanitized stem
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}
	return data, nil
}

// readCheckoutReleases reads the retired release files. At HEAD it lists them
// with `git ls-tree`; otherwise it globs the working tree.
func readCheckoutReleases(ctx context.Context, projectDir string) ([]release.Release, error) {
	var paths []string
	prefix, isGit := gitPrefix(ctx, projectDir)
	if isGit {
		out, err := gitShow(ctx, projectDir, "HEAD:"+prefix+retiredReleasesDirRel)
		if err == nil {
			for _, name := range ledgerLines(string(out)) {
				if strings.HasSuffix(name, ".json") {
					paths = append(paths, filepath.Join(retiredReleasesDirRel, name))
				}
			}
		}
	}
	if paths == nil {
		matches, err := filepath.Glob(filepath.Join(projectDir, retiredReleasesDirRel, "*.json"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if rel, err := filepath.Rel(projectDir, m); err == nil {
				paths = append(paths, rel)
			}
		}
	}
	var out []release.Release
	for _, p := range paths {
		data, err := readCheckoutFile(ctx, projectDir, filepath.ToSlash(p))
		if err != nil || len(data) == 0 {
			continue
		}
		// The retired files are statefile envelopes; the release is
		// under its label. A file that does not parse is skipped, for
		// the same reason a malformed promotion line is.
		var env struct {
			Release *release.Release `json:"release"`
		}
		if json.Unmarshal(data, &env) == nil && env.Release != nil && env.Release.Version != "" {
			out = append(out, *env.Release)
			continue
		}
		var r release.Release
		if json.Unmarshal(data, &r) == nil && r.Version != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// gitPrefix is the project's path within its repository, with a trailing
// slash, and whether it is a git checkout at all. Asking git — rather than
// computing it against --show-toplevel — is what keeps a symlinked checkout
// path (macOS's /var → /private/var) from reading as "outside the
// repository": git resolves both sides the same way.
func gitPrefix(ctx context.Context, projectDir string) (string, bool) {
	out, err := gitLedgerCmd(ctx, projectDir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func gitShow(ctx context.Context, projectDir, spec string) ([]byte, error) {
	return gitLedgerCmd(ctx, projectDir, "show", spec)
}

func gitLedgerCmd(ctx context.Context, projectDir string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, ledgerGitReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = projectDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
