package bundle

// Caching by digest, and the local OCI layout.
//
// A DIGEST NAMES BYTES. That single fact is what lets this cache skip every
// freshness check a normal HTTP cache needs: if a reference still resolves to
// a digest already on disk, the cached bytes ARE the current bytes,
// necessarily, with no TTL to tune and no staleness window. There is nothing
// to revalidate because content-addressed content cannot change under its
// name.
//
// So the hot path is exactly ONE round trip:
//
//	Resolve(ref)  →  descriptor (an HTTP HEAD against the manifest)
//	digest already unpacked?  →  return it, no pull
//
// and a pull happens only on a digest this machine has never seen. A deploy
// of an unchanged bundle therefore costs one HEAD rather than a full
// download.
//
// A reference that is ITSELF a digest (`repo@sha256:…`) still goes through
// Resolve. It could be short-circuited, and deliberately is not: the resolve
// also proves the bundle is still PRESENT in the registry, and applying from
// a locally-cached bundle that had been deleted upstream would converge an
// env onto bytes nobody could reproduce — doc §13's F-4, which BeginApply
// refuses for the same reason.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"

	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/pkg/release"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// CacheDirRel is where unpacked bundles live, under the same .forge/ tree as
// every other piece of forge runtime state (so the single existing .forge/
// gitignore rule already covers it).
const CacheDirRel = statefile.DirRel + "/bundles"

// docFileName is the bundle document, written beside the unpacked manifests
// so a cache entry is self-describing. A FIXED name rather than a search: a
// loop that picked "the first JSON it found" would change behaviour the day a
// bundle grew a second one.
const docFileName = "bundle.json"

// Cache is a digest-keyed store of unpacked bundles on local disk.
//
// The zero value is not usable; construct with [NewCache].
type Cache struct {
	root string
	// mu serialises unpacks so two concurrent deploys of the same digest
	// do not both write the same tree. The check-then-unpack sequence is
	// not atomic on its own, and O_EXCL inside Unpack turns the race into
	// a hard error rather than a silent interleave — which would be
	// correct but would fail a caller that did nothing wrong.
	mu sync.Mutex
}

// NewCache returns a Cache rooted under projectDir.
func NewCache(projectDir string) (*Cache, error) {
	if projectDir == "" {
		projectDir = "."
	}
	root, err := filepath.Abs(filepath.Join(projectDir, CacheDirRel))
	if err != nil {
		return nil, fmt.Errorf("resolve bundle cache dir: %w", err)
	}
	return &Cache{root: root}, nil
}

// Stats records what one [Cache.Get] actually did, so a caller (and a test)
// can tell a cache hit from a pull WITHOUT inferring it from timing.
//
// This exists because "an unchanged digest must cost one HEAD, not a full
// pull" is invisible from the outside: both paths return the same bundle. A
// test asserting the hit path by measuring duration would pass against an
// implementation that pulled every time, on a fast enough machine. Recording
// the decision makes the property checkable rather than believed.
type Stats struct {
	// Resolved is true when the reference was resolved (the HEAD). Always
	// true on success.
	Resolved bool
	// Pulled is true when content was transferred. FALSE ON A CACHE HIT —
	// that is the property worth asserting.
	Pulled bool
	// Digest is the resolved manifest digest.
	Digest string
}

// Cached is one unpacked bundle on disk.
type Cached struct {
	// Digest is the bundle's identity.
	Digest string
	// Doc is the bundle document.
	Doc release.BundleDoc
	// Dir is the directory the manifest layer was unpacked into:
	// `manifests/<cluster>/…`.
	Dir string
	// ManifestFiles are the manifest-layer paths relative to Dir, in APPLY
	// ORDER (the render order the filenames encode).
	ManifestFiles []string
}

// pathFor returns the on-disk directory for one digest.
//
// The digest is split on its ':' so the tree is `bundles/sha256/<hex>/`
// rather than a single directory holding a colon in every name — colons are
// legal on POSIX and a nuisance elsewhere. SafeSegment is applied to both
// halves even though a validated digest cannot contain a separator, because
// this path is composed from a value that arrived over the network and the
// guard costs nothing.
func (c *Cache) pathFor(digest string) (string, error) {
	if !release.ValidDigest(digest) {
		return "", fmt.Errorf("%w: refusing to cache under non-canonical digest %q", release.ErrInvalid, digest)
	}
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return "", fmt.Errorf("%w: digest %q has no algorithm prefix", release.ErrInvalid, digest)
	}
	return filepath.Join(c.root, statefile.SafeSegment(algo), statefile.SafeSegment(hex)), nil
}

// Get resolves reference, returns the cached bundle when its digest is
// already unpacked, and otherwise fetches, unpacks and caches it.
func (c *Cache) Get(ctx context.Context, res Resolver, reference string) (Cached, Stats, error) {
	var stats Stats

	// The HEAD. One round trip, and on the hot path the ONLY one.
	desc, err := res.Resolve(ctx, reference)
	if err != nil {
		return Cached{}, stats, fmt.Errorf("resolve bundle %q: %w", reference, err)
	}
	stats.Resolved = true
	digest := desc.Digest.String()
	stats.Digest = digest

	dir, perr := c.pathFor(digest)
	if perr != nil {
		return Cached{}, stats, perr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if hit, ok, herr := readCached(dir, digest); herr != nil {
		return Cached{}, stats, herr
	} else if ok {
		// CACHE HIT. No blob was transferred — the property Stats.Pulled
		// exists to make observable.
		return hit, stats, nil
	}

	fetched, ferr := Fetch(ctx, res, reference)
	if ferr != nil {
		return Cached{}, stats, ferr
	}
	stats.Pulled = true

	// Unpack into a TEMPORARY sibling and rename into place, so an
	// interrupted pull cannot leave a half-written tree under a digest the
	// next run would then treat as a complete cache hit. A partial tree
	// keyed by a valid digest is worse than no cache: the digest would be
	// a promise about bytes the directory does not hold.
	if err := os.MkdirAll(filepath.Dir(dir), unpackDirMode); err != nil {
		return Cached{}, stats, fmt.Errorf("create cache dir: %w", err)
	}
	staging, terr := os.MkdirTemp(filepath.Dir(dir), ".partial-*")
	if terr != nil {
		return Cached{}, stats, fmt.Errorf("create staging dir: %w", terr)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	files, uerr := Unpack(newReader(fetched.Manifests), staging)
	if uerr != nil {
		return Cached{}, stats, uerr
	}
	docBytes, merr := json.Marshal(fetched.Doc)
	if merr != nil {
		return Cached{}, stats, merr
	}
	if werr := atomicfile.WriteFile(filepath.Join(staging, docFileName), docBytes, unpackFileMode); werr != nil {
		return Cached{}, stats, fmt.Errorf("write %s: %w", docFileName, werr)
	}

	out := Cached{Digest: digest, Doc: fetched.Doc, Dir: dir, ManifestFiles: files}
	if rerr := os.Rename(staging, dir); rerr != nil {
		// A concurrent process may have won the race and populated the
		// directory with the SAME digest — which by definition holds the
		// same bytes, so its copy is as good as ours.
		if _, serr := os.Stat(dir); serr == nil {
			return out, stats, nil
		}
		return Cached{}, stats, fmt.Errorf("publish bundle %s into cache: %w", digest, rerr)
	}
	return out, stats, nil
}

// readCached returns the bundle already unpacked under dir, if any.
//
// A missing directory is a clean miss. A directory that EXISTS but whose
// document is unreadable is an ERROR rather than a miss: silently re-pulling
// over a corrupt cache entry would hide the corruption forever, and this is
// the one place that would notice it.
func readCached(dir, digest string) (Cached, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, docFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Cached{}, false, nil
		}
		return Cached{}, false, fmt.Errorf("read cached bundle %s: %w", digest, err)
	}
	doc, derr := release.DecodeBundleDoc(raw)
	if derr != nil {
		return Cached{}, false, fmt.Errorf("cached bundle %s is unreadable (remove %s to re-pull): %w", digest, dir, derr)
	}
	files, lerr := listManifestFiles(dir)
	if lerr != nil {
		return Cached{}, false, lerr
	}
	return Cached{Digest: digest, Doc: doc, Dir: dir, ManifestFiles: files}, true, nil
}

// listManifestFiles walks the unpacked manifests tree in lexicographic order,
// which the filenames make the apply order (see [manifestEntryName]).
func listManifestFiles(dir string) ([]string, error) {
	root := filepath.Join(dir, manifestsPrefix)
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list cached manifests under %s: %w", root, err)
	}
	return out, nil
}

// ─── The local OCI layout ───────────────────────────────────────────────────

// LocalLayout is an OCI image layout directory (`oci-layout` +
// `blobs/sha256/…` + `index.json`): where a bundle goes when it is not
// pushed.
//
// A `local` env's bundle is written here even when its control plane is
// reachable, because it is not SHIPPED — doc §4.3. The machine ledger (F3)
// owns the directory and passes its path in; this type does not know where
// the ledger lives, and deliberately does not import it. The dependency runs
// ledger → bundle, so a bundle can be written to any layout a caller names
// and the layout root stays one decision made in one place.
type LocalLayout struct {
	store *oci.Store
	root  string
}

// NewLocalLayout opens (or creates) an OCI image layout rooted at dir.
func NewLocalLayout(dir string) (*LocalLayout, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("bundle: local layout needs a directory")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("bundle: resolve local layout %q: %w", dir, err)
	}
	store, err := oci.New(root)
	if err != nil {
		return nil, fmt.Errorf("bundle: open local layout %s: %w", root, err)
	}
	return &LocalLayout{store: store, root: root}, nil
}

// Root is the layout directory.
func (l *LocalLayout) Root() string { return l.root }

// Reference is how a ledger records a bundle held in a local layout:
// `oci-layout:<dir>@<digest>`. Both halves are needed — the digest alone does
// not say which machine's layout holds the blobs, and a file ledger is
// machine-scoped.
func (l *LocalLayout) Reference(digest string) string {
	return "oci-layout:" + l.root + "@" + digest
}

// Write stores a built bundle in the layout and tags it with the env name, so
// `<env>` resolves to the newest bundle written for it while every bundle
// stays addressable by digest.
func (l *LocalLayout) Write(ctx context.Context, b Bundle) (string, error) {
	digest, err := Push(ctx, l.store, b)
	if err != nil {
		return "", err
	}
	manifestDesc, ok := b.descriptorOf(ocispec.MediaTypeImageManifest)
	if !ok {
		return "", fmt.Errorf("%w: bundle has no manifest descriptor", release.ErrInvalid)
	}
	if err := l.store.Tag(ctx, manifestDesc, b.Doc.Env); err != nil {
		return "", fmt.Errorf("tag local bundle %s as %s: %w", digest, b.Doc.Env, err)
	}
	return digest, nil
}

// Resolve and Fetch make a LocalLayout a [Resolver], so [Fetch] reads a local
// bundle through exactly the path a registry bundle goes through — same
// verification, same bounds, same refusals. A second read path for local
// bundles is how "it works from the registry but not from the layout" starts.
func (l *LocalLayout) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	return l.store.Resolve(ctx, reference)
}

// Fetch reads one blob out of the local layout. See [LocalLayout.Resolve] for
// why both exist.
func (l *LocalLayout) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	return l.store.Fetch(ctx, target)
}

// descriptorOf returns the descriptor of the blob with that media type.
func (b Bundle) descriptorOf(mediaType string) (ocispec.Descriptor, bool) {
	for _, bl := range b.blobs {
		if bl.desc.MediaType == mediaType {
			return bl.desc, true
		}
	}
	return ocispec.Descriptor{}, false
}
