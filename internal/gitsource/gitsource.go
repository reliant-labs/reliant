// Package gitsource resolves a declared cross-repo source — "that repo at
// that ref" — to a directory on the local filesystem, so a component whose
// code lives in ANOTHER repository can be built from a checkout that
// contains only this one.
//
// Background: every cross-repo mechanism forge had was filesystem-only. A
// frontend declared `path = "../reliant/web"` builds on a laptop that
// happens to have the sibling checkout and fails in CI, where
// actions/checkout clones a single repository. Worse than the failure is
// the success: when the sibling IS present, the version that ships is
// whatever happened to be checked out, so two machines produce different
// artifacts from identical commits. The Go half of the same dependency has
// been a pinned, proxy-resolved module all along; only the frontend and
// image halves were an unpinned path.
//
// A GitSource closes that asymmetry. `repo` + `ref` + optional `subdir` is
// a reviewable, promotable pin that resolves identically on a laptop, in a
// container, and in CI — the same property go.mod gives the Go half.
//
// # The cache
//
// Fetches land in a machine-local cache keyed by repo AND ref
// (<UserCacheDir>/forge/sources/<repo-slug>-<digest>), so a second build of
// the same pin does no network work at all, and two pins of the same repo
// coexist rather than fighting over one directory. A materialized checkout
// records what produced it in .forge-source.json; the presence of that file
// is what makes a directory a cache HIT, so a fetch interrupted halfway
// leaves no directory that a later build would mistake for complete.
//
// Immutability is the caller's to choose. A full commit sha, or a tag under
// a project that does not move tags, gives a cache that can never be stale.
// A branch ref is cached the same way — forge does not re-fetch a branch on
// every build, because a dependency that silently changes underneath a
// build is the exact non-reproducibility this package exists to remove.
// Delete the cache entry (or Refresh) to pick up a moved ref.
//
// # The local override — why it is a file and not a mode
//
// Pinning without an escape hatch would force every one-line frontend edit
// through a push, a tag and a re-fetch, which is how a feature gets
// resented rather than adopted. So a source may be overridden to a local
// directory, the same way a gitignored go.work bridges a project's Go side
// to a local forge checkout during development (`forge project new
// --link-forge`).
//
// The override lives in `.forge/source-overrides.yaml` — machine-local,
// gitignored, and read only when present. Two properties are deliberate:
//
//   - It is EXPLICIT. forge never auto-adopts a sibling checkout it happens
//     to find on disk. Silent adoption would reintroduce the unpinned build
//     this package exists to eliminate, and it would do it invisibly, on the
//     one machine (a maintainer's laptop) least likely to notice.
//   - It is DECLARATIVE and out of the build command. The override is state
//     in a file under the developer's control, not a flag that has to be
//     remembered on every invocation or an env var that leaks into a child
//     process.
//
// Because the file is never committed, an override cannot follow a change
// into CI: the pin is what builds there, always.
package gitsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Source is a declared cross-repo dependency: a repository, a ref to pin
// it at, and optionally a subdirectory within it. It mirrors the
// `forge.GitSource` KCL schema and the `source:` block in forge.yaml.
type Source struct {
	// Repo is the repository to fetch. The canonical spelling is
	// host/owner/name ("github.com/reliant-labs/reliant"), which forge
	// expands to an https clone URL. An explicit https:// or ssh:// URL,
	// or scp-style git@host:owner/name, is passed through untouched — a
	// project on a private host must not be forced through a spelling
	// forge invented.
	Repo string `yaml:"repo" json:"repo"`

	// Ref is the tag, branch, or full commit sha to check out. Required:
	// a source with no ref is the unpinned filesystem dependency this
	// type exists to replace, so forge refuses to guess a default branch.
	Ref string `yaml:"ref" json:"ref"`

	// Subdir is the path WITHIN the repository that this component's
	// source lives at ("web" for a repo whose SPA is in web/). Empty
	// means the repository root.
	Subdir string `yaml:"subdir,omitempty" json:"subdir,omitempty"`
}

// IsZero reports whether the source is entirely unset — the common case
// of a component that declares a filesystem path instead.
func (s Source) IsZero() bool {
	return s.Repo == "" && s.Ref == "" && s.Subdir == ""
}

// String renders the source the way it reads in a log line or an error.
func (s Source) String() string {
	out := s.Repo + "@" + s.Ref
	if s.Subdir != "" {
		out += "//" + s.Subdir
	}
	return out
}

// refRE is deliberately permissive — git refs allow a lot — while still
// rejecting the shapes that would be interpreted as flags or shell by the
// git invocation, or that would escape the cache directory.
var refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]*$`)

// repoRE matches the canonical host/owner/name spelling.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*(/[A-Za-z0-9._-]+)+$`)

// Validate reports why a source cannot be used, or nil. The errors name
// the field and what a correct value looks like: a malformed pin is
// almost always a typo, and the fix is a one-line edit the message can
// spell out.
func (s Source) Validate() error {
	switch {
	case s.Repo == "" && s.Ref == "":
		return errors.New("source is empty: `repo` and `ref` are both required (e.g. repo: github.com/org/app, ref: v1.2.3)")
	case s.Repo == "":
		return errors.New("source.repo is required (e.g. github.com/org/app)")
	case s.Ref == "":
		return fmt.Errorf("source.ref is required for repo %q — a tag, branch, or commit sha; forge does not default to a branch, because an unpinned cross-repo dependency is what makes a build unreproducible", s.Repo)
	}
	if !isURLRepo(s.Repo) && !repoRE.MatchString(s.Repo) {
		return fmt.Errorf("source.repo %q is not a recognized repository: use host/owner/name (github.com/org/app), an https:// or ssh:// URL, or git@host:owner/name", s.Repo)
	}
	if !refRE.MatchString(s.Ref) {
		return fmt.Errorf("source.ref %q is not a valid git ref (letters, digits, and . _ / + - only)", s.Ref)
	}
	if s.Subdir != "" && !isRepoRelative(s.Subdir) {
		return fmt.Errorf("source.subdir %q must be a relative path inside the repository", s.Subdir)
	}
	return nil
}

// isRepoRelative reports whether subdir names a directory inside the
// repository, judged the SAME way on every OS.
//
// subdir is a path within a git tree — slash-separated, like every path git
// records — not a host path, and the same forge.yaml is validated on every
// developer's machine. filepath.IsAbs made the verdict depend on the host:
// "/etc" passed on Windows (rooted, but no volume, so not "absolute"), and
// `C:\x` passed everywhere else. So a backslash is read as the separator it
// is on Windows, and a leading separator, a drive letter, or a ".." that
// escapes the tree is refused on every host.
func isRepoRelative(subdir string) bool {
	clean := path.Clean(strings.ReplaceAll(subdir, `\`, "/"))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	if len(clean) >= 2 && clean[1] == ':' {
		return false // C:, C:/x, C:x — a drive, not a directory in the repo
	}
	return true
}

// isURLRepo reports whether the repo is already an explicit clone URL
// (any scheme, or the scp-style git@host:path form) rather than the
// canonical host/owner/name shorthand.
func isURLRepo(repo string) bool {
	if strings.Contains(repo, "://") {
		return true
	}
	// scp-style: git@github.com:org/app.git — a colon before any slash.
	if i := strings.Index(repo, ":"); i > 0 && !strings.Contains(repo[:i], "/") {
		return true
	}
	return false
}

// CloneURL is the URL forge hands to git. The canonical host/owner/name
// shorthand expands to https; an explicit URL passes through so a private
// host, an ssh remote, or a credential-helper-backed URL all keep working.
func (s Source) CloneURL() string {
	if isURLRepo(s.Repo) {
		return s.Repo
	}
	return "https://" + strings.TrimSuffix(s.Repo, ".git") + ".git"
}

// slug renders the repo as a filesystem-safe, human-readable directory
// prefix, so a cache directory can be identified by eye.
func (s Source) slug() string {
	base := s.Repo
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[i+3:]
	}
	base = strings.TrimSuffix(base, ".git")
	base = strings.NewReplacer("@", "-", ":", "-", "/", "-", " ", "-").Replace(base)
	base = strings.Trim(base, "-")
	if len(base) > 64 {
		base = base[:64]
	}
	if base == "" {
		base = "repo"
	}
	return base
}

// CacheKey is the cache directory NAME for this source: a readable slug
// plus a digest of the exact repo+ref.
//
// The digest covers repo and ref but NOT subdir — the whole repository is
// materialized once and every subdir of the same pin is a view into it,
// so a project depending on two directories of one repo fetches once.
func (s Source) CacheKey() string {
	sum := sha256.Sum256([]byte(s.CloneURL() + "\x00" + s.Ref))
	return s.slug() + "-" + hex.EncodeToString(sum[:])[:12]
}

// Metadata is the record a materialized cache entry carries. Its presence
// is what marks the entry COMPLETE: a fetch writes it last, so an
// interrupted fetch leaves a directory no later build treats as a hit.
type Metadata struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// Commit is the sha the ref resolved to at fetch time. This is what
	// makes a branch-ref build auditable after the fact — the pin says
	// "main", the metadata says which main.
	Commit string `json:"commit,omitempty"`
}

// MetadataFile is the completion marker inside a cache entry.
const MetadataFile = ".forge-source.json"

// Resolver turns declared sources into directories on disk.
//
// The zero value is not usable; construct one with NewResolver.
type Resolver struct {
	// cacheRoot is the directory cache entries live under.
	cacheRoot string
	// fetcher performs the actual materialization on a cache miss.
	fetcher Fetcher
	// overrides maps a repo (canonical spelling) to a local directory
	// that stands in for it. Loaded from .forge/source-overrides.yaml.
	overrides map[string]string
	// projectDir anchors relative override paths, so an override reads
	// "../reliant" the way the filesystem path it replaces did.
	projectDir string
	// log receives one line per resolution. nil discards.
	log func(string)
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithFetcher replaces the git fetcher. Tests inject a fake; nothing in
// production needs this.
func WithFetcher(f Fetcher) Option { return func(r *Resolver) { r.fetcher = f } }

// WithCacheRoot overrides the machine-local cache location. Tests point
// it at a temp dir so they never touch the developer's real cache.
func WithCacheRoot(dir string) Option { return func(r *Resolver) { r.cacheRoot = dir } }

// WithOverrides sets the repo→local-directory override map directly,
// bypassing the on-disk file. Used by tests and by callers that have
// already loaded the file themselves.
func WithOverrides(m map[string]string) Option {
	return func(r *Resolver) { r.overrides = m }
}

// WithLogger routes the resolver's one-line-per-source reporting.
func WithLogger(fn func(string)) Option { return func(r *Resolver) { r.log = fn } }

// NewResolver builds a resolver for a project rooted at projectDir.
//
// Unless overridden by an Option it uses the real git fetcher, the
// machine-local cache under the user cache dir, and the override file at
// <projectDir>/.forge/source-overrides.yaml. A missing or unreadable
// override file is not an error — the overwhelmingly common case is that
// there is none.
func NewResolver(projectDir string, opts ...Option) (*Resolver, error) {
	r := &Resolver{projectDir: projectDir, fetcher: GitFetcher{}}
	for _, opt := range opts {
		opt(r)
	}
	if r.cacheRoot == "" {
		root, err := DefaultCacheRoot()
		if err != nil {
			return nil, err
		}
		r.cacheRoot = root
	}
	if r.overrides == nil {
		ov, err := LoadOverrides(projectDir)
		if err != nil {
			return nil, err
		}
		r.overrides = ov
	}
	return r, nil
}

// DefaultCacheRoot is the machine-local root every fetched source lives
// under, shared across projects: one fetch of a given pin serves every
// project on the machine that declares it.
func DefaultCacheRoot() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir for the forge source cache: %w", err)
	}
	return filepath.Join(dir, "forge", "sources"), nil
}

// Resolution reports how a source was resolved, so callers can tell a
// reproducible pin from a local override in their own output — which is
// the whole point of surfacing it.
type Resolution struct {
	// Dir is the directory the component's source is at, including any
	// subdir. This is what a build shells into.
	Dir string
	// Overridden is true when a local override stood in for the pin.
	Overridden bool
	// Cached is true when a fetched pin was already materialized, so no
	// network work happened.
	Cached bool
	// Commit is the resolved sha, when known.
	Commit string
}

// Resolve returns the directory holding the source's code, fetching it
// into the cache if needed.
//
// Precedence is: local override, then cache, then fetch. An override
// wins over a pin unconditionally and by design — it is the developer
// saying "build what is in front of me" — and it is reported, never
// silent, because a build that quietly stopped honoring its pin is
// exactly the failure this package removes.
func (r *Resolver) Resolve(ctx context.Context, src Source) (Resolution, error) {
	if err := src.Validate(); err != nil {
		return Resolution{}, err
	}

	if dir, ok := r.overrideFor(src); ok {
		full := dir
		if src.Subdir != "" {
			full = filepath.Join(dir, filepath.FromSlash(src.Subdir))
		}
		if _, err := os.Stat(full); err != nil {
			return Resolution{}, fmt.Errorf(
				"source %s is overridden to %s, but that directory does not exist — fix or remove the entry in %s",
				src, full, filepath.Join(OverridesDirName, OverridesFileName))
		}
		r.logf("  source %s → %s (local override — NOT the pinned ref)", src, full)
		return Resolution{Dir: full, Overridden: true}, nil
	}

	entry := filepath.Join(r.cacheRoot, src.CacheKey())
	meta, hit := readMetadata(entry)
	if !hit {
		// A directory without the completion marker is the debris of an
		// interrupted fetch. Clear it rather than fetching into it.
		if err := os.RemoveAll(entry); err != nil {
			return Resolution{}, fmt.Errorf("clear incomplete source cache entry %s: %w", entry, err)
		}
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			return Resolution{}, fmt.Errorf("create source cache root: %w", err)
		}
		r.logf("  source %s → fetching into %s", src, entry)
		commit, err := r.fetcher.Fetch(ctx, src, entry)
		if err != nil {
			_ = os.RemoveAll(entry)
			return Resolution{}, fmt.Errorf("fetch %s: %w", src, err)
		}
		meta = Metadata{Repo: src.Repo, Ref: src.Ref, Commit: commit}
		if err := writeMetadata(entry, meta); err != nil {
			_ = os.RemoveAll(entry)
			return Resolution{}, err
		}
	} else {
		r.logf("  source %s → %s (cached)", src, entry)
		// Record the hit so machine-wide maintenance can tell a pin that
		// is still in rotation from one abandoned two bumps ago. A fetch
		// needs no touch: writeMetadata just set the mtime to now.
		// Failure is logged, never returned — see touchUsed.
		if err := touchUsed(entry, time.Now()); err != nil {
			r.logf("  source %s: could not record cache use (%v) — entry may be evicted earlier than intended", src, err)
		}
	}

	// Also runs on older cache hits: the resolver's marker is not source
	// dirt. Never touch an override checkout's excludes (returned above).
	if err := excludeCacheMetadata(entry); err != nil {
		return Resolution{}, err
	}
	full := entry
	if src.Subdir != "" {
		full = filepath.Join(entry, filepath.FromSlash(src.Subdir))
		if _, err := os.Stat(full); err != nil {
			return Resolution{}, fmt.Errorf(
				"source %s: subdir %q does not exist in %s at ref %s",
				src, src.Subdir, src.Repo, src.Ref)
		}
	}
	return Resolution{Dir: full, Cached: hit, Commit: meta.Commit}, nil
}

// Refresh drops the cache entry for a source so the next Resolve
// re-fetches it — the supported way to pick up a moved branch or tag.
// A source that was never fetched is a no-op.
func (r *Resolver) Refresh(src Source) error {
	if err := src.Validate(); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(r.cacheRoot, src.CacheKey()))
}

// overrideFor returns the local directory standing in for this source's
// repo, if one is declared. Lookup is by repo, not by repo+ref: an
// override means "use my working copy instead of any pin", and requiring
// the developer to restate the ref they are deliberately bypassing would
// make the override break every time the pin is bumped.
func (r *Resolver) overrideFor(src Source) (string, bool) {
	if len(r.overrides) == 0 {
		return "", false
	}
	dir, ok := r.overrides[normalizeRepo(src.Repo)]
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.projectDir, filepath.FromSlash(dir))
	}
	return filepath.Clean(dir), true
}

func (r *Resolver) logf(format string, args ...any) {
	if r.log == nil {
		return
	}
	r.log(fmt.Sprintf(format, args...))
}

// normalizeRepo reduces the spellings of one repository to a single key,
// so an override written as `github.com/org/app` matches a source
// declared as `https://github.com/org/app.git`.
func normalizeRepo(repo string) string {
	out := repo
	if i := strings.Index(out, "://"); i >= 0 {
		out = out[i+3:]
	}
	if i := strings.Index(out, "@"); i >= 0 && !strings.Contains(out[:i], "/") {
		out = out[i+1:]
	}
	out = strings.Replace(out, ":", "/", 1)
	out = strings.TrimSuffix(out, ".git")
	return strings.Trim(strings.ToLower(out), "/")
}

func readMetadata(entry string) (Metadata, bool) {
	data, err := os.ReadFile(filepath.Join(entry, MetadataFile))
	if err != nil {
		return Metadata{}, false
	}
	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return Metadata{}, false
	}
	return m, true
}

func writeMetadata(entry string, m Metadata) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode source metadata: %w", err)
	}
	path := filepath.Join(entry, MetadataFile)
	if err := atomicfile.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write source metadata %s: %w", path, err)
	}
	return nil
}
