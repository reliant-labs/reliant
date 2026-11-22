// Package sbom generates the software bill of materials forge records for
// every artifact a release ships: a CycloneDX JSON document of THE BYTES
// THAT SHIP, produced by forge's pinned syft.
//
// What syft reads depends on the artifact (Kind):
//
//   - an image forge built: the local docker daemon by digest, falling back
//     to the registry when the daemon does not hold it (a buildx push that
//     never loaded the image, a cut on another machine);
//   - a prebuilt image (a hosted backend, a mirrored third-party image): the
//     registry, by digest;
//   - a host binary: the file (Go buildinfo lists every linked module);
//   - a static site, an npm package, a Go module: the lockfile or manifest,
//     because a bundle carries no package metadata (syft finds nothing in a
//     built dist/). Dev dependencies are excluded, syft's default;
//   - a source tree (a record's --sbom-source): syft's directory scan of
//     the lockfiles and manifests in it.
//
// An SBOM a pipeline produced itself is not generated here; [Identify]
// checks that one and names its format.
//
// A failure never fails the caller: [Generator.Generate] returns an
// [release.SBOMStatusUnavailable] ref with the reason, which a reader shows
// as "not scanned", never as "clean".
//
// Output is cached by what syft read (the image digest, or the hash of the
// file) and the generator version, so a re-cut, or a deploy that cuts the
// same images again, runs no syft at all ([Cache]).
package sbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Format is the format every SBOM this package writes is in. syft is asked
// for this exact CycloneDX version (its own default moves with syft
// releases: v1.57.2 writes 1.7 unless told otherwise), so the recorded
// format is a fact about the bytes, not about whichever syft ran.
const Format = release.SBOMFormatCycloneDXJSON

// syftOutput is Format in syft's `-o` spelling, and specVersion is the
// CycloneDX version every document is checked to carry.
const (
	syftOutput  = "cyclonedx-json@1.6"
	specVersion = "1.6"
)

// Kind is what syft reads for one artifact.
type Kind string

const (
	// KindImage is an image forge built: the docker daemon first, then the
	// registry. The result's Source says which answered.
	KindImage Kind = "image"
	// KindRegistry is an image read from its registry by digest.
	KindRegistry Kind = "registry"
	// KindBinary is a binary on disk.
	KindBinary Kind = "binary"
	// KindLockfile is a lockfile or manifest on disk (package-lock.json,
	// pnpm-lock.yaml, yarn.lock, go.mod).
	KindLockfile Kind = "lockfile"
	// KindDir is a source tree: syft's directory scan, which reads the
	// lockfiles and manifests it finds (dev dependencies excluded). The
	// tree has no one digest, so its Subject is optional and, when empty,
	// nothing is cached.
	KindDir Kind = "dir"
)

// Target is one artifact to describe.
type Target struct {
	// Name is the artifact's name in the release. It becomes the SBOM's
	// source name, so the document never carries a path from this machine.
	Name string
	Kind Kind
	// Ref is what syft reads: `repo@sha256:…` for an image, a path for a
	// binary or lockfile.
	Ref string
	// Subject is the digest of the bytes syft reads, the cache key: the
	// image digest, or `sha256:<hex>` of the file. Empty for a file means
	// "hash Ref"; an image must name it.
	Subject string
	// Version is recorded as the SBOM's source version (optional).
	Version string
	// Platform selects one platform of a multi-platform image
	// (`linux/amd64`); empty lets syft choose. Part of the cache key.
	Platform string
}

// Result is one target's outcome. Ref is always set; Bytes only when
// Ref.Status is [release.SBOMStatusOK].
type Result struct {
	Target Target
	Ref    release.SBOMRef
	Bytes  []byte
	// Cached is true when the bytes came from the cache and no syft ran.
	Cached bool
}

// Tool resolves the syft binary to run: its path and the pinned version
// (`v1.57.2`). An error means no SBOM can be produced.
type Tool func(ctx context.Context) (path, version string, err error)

// Generator produces SBOMs. The zero value with Tool set works: it execs
// syft, caches nothing, and describes one target at a time with no deadline
// but ctx's. The release cut runs DefaultConcurrency targets at once, each
// bounded by DefaultTimeout.
type Generator struct {
	Tool   Tool
	Runner Runner
	// Cache, when set, is read before syft runs and written after.
	Cache *Cache
	// Concurrency bounds how many syft processes run at once (≤0: one).
	Concurrency int
	// Timeout bounds one target's syft run (≤0: only ctx bounds it).
	Timeout time.Duration
	// RegistryAuth, when set, supplies forge's own credential for a
	// registry syft reads an image from (registry_auth.go). nil leaves every
	// registry read on the ambient docker credential store.
	RegistryAuth RegistryAuth
}

const (
	// DefaultConcurrency is how many syft processes the release cut runs at
	// once.
	DefaultConcurrency = 4
	// DefaultTimeout bounds one target's syft run at the release cut. It is
	// generous: a registry read pulls the whole image (13.7 s for 115 MB
	// measured), and a slow link multiplies that.
	DefaultTimeout = 10 * time.Minute
)

// Generate describes every target, at most Concurrency at once, and returns
// one Result per target in the same order. It never fails: a target it
// could not describe gets an unavailable ref saying why.
func (g *Generator) Generate(ctx context.Context, targets []Target) []Result {
	results := make([]Result, len(targets))
	if len(targets) == 0 {
		return results
	}
	if g.Tool == nil {
		for i, t := range targets {
			results[i] = unavailable(t, "", "no SBOM generator is configured")
		}
		return results
	}
	bin, version, err := g.Tool(ctx)
	if err != nil {
		for i, t := range targets {
			results[i] = unavailable(t, "", "syft is unavailable: "+oneLine(err.Error()))
		}
		return results
	}
	generator := GeneratorName(version)

	n := max(g.Concurrency, 1)
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = g.one(ctx, bin, generator, t)
		}(i, t)
	}
	wg.Wait()
	return results
}

// GeneratorName is the SBOMRef.Generator value for a syft version:
// `syft@v1.57.2`.
func GeneratorName(version string) string {
	v := strings.TrimSpace(version)
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return "syft@" + v
}

func (g *Generator) one(ctx context.Context, bin, generator string, t Target) Result {
	subject := t.Subject
	switch t.Kind {
	case KindBinary, KindLockfile:
		if subject == "" {
			sum, err := fileDigest(t.Ref)
			if err != nil {
				return unavailable(t, sourceFor(t.Kind), err.Error())
			}
			subject = sum
		}
	case KindImage, KindRegistry:
		if !release.ValidDigest(subject) {
			return unavailable(t, sourceFor(t.Kind), fmt.Sprintf("image %s has no digest to describe", t.Ref))
		}
	case KindDir:
		if fi, err := os.Stat(t.Ref); err != nil || !fi.IsDir() {
			return unavailable(t, sourceFor(t.Kind), fmt.Sprintf("%s is not a directory", t.Ref))
		}
	default:
		return unavailable(t, "", fmt.Sprintf("unknown SBOM target kind %q", t.Kind))
	}
	t.Subject = subject

	key := CacheKey{Generator: generator, Subject: t.Subject, Platform: t.Platform, SourceName: t.Name, SourceVersion: t.Version}
	if data, source, ok := g.Cache.Get(key); ok {
		return okResult(t, generator, source, data, true)
	}

	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}

	data, source, err := g.scan(ctx, bin, t)
	if err != nil {
		return unavailable(t, source, err.Error())
	}
	// A cache write failure costs the next cut a syft run, nothing more.
	_ = g.Cache.Put(key, source, data)
	return okResult(t, generator, source, data, false)
}

// scan runs syft for t and returns the checked document and the source that
// answered. An image forge built is read from the daemon first: that is the
// image it just built, with no download. The daemon resolves `repo@digest`
// only when it holds that exact pushed manifest, so a hit describes the
// recorded bytes; anything else falls back to the registry.
func (g *Generator) scan(ctx context.Context, bin string, t Target) ([]byte, string, error) {
	switch t.Kind {
	case KindImage:
		data, derr := g.run(ctx, bin, t, "docker:"+t.Ref, nil)
		if derr == nil {
			return data, release.SBOMSourceImage, nil
		}
		if ctx.Err() != nil {
			return nil, release.SBOMSourceImage, fmt.Errorf("syft on %s: %w", t.Ref, ctx.Err())
		}
		env, note := g.registryEnv(ctx, t.Ref)
		data, rerr := g.run(ctx, bin, t, "registry:"+t.Ref, env)
		if rerr != nil {
			return nil, release.SBOMSourceRegistry, fmt.Errorf("syft could not read %s from the docker daemon (%s) or its registry (%s)%s",
				t.Ref, oneLine(derr.Error()), oneLine(rerr.Error()), withNote(note))
		}
		return data, release.SBOMSourceRegistry, nil
	case KindRegistry:
		env, note := g.registryEnv(ctx, t.Ref)
		data, err := g.run(ctx, bin, t, "registry:"+t.Ref, env)
		if err != nil {
			return nil, release.SBOMSourceRegistry, fmt.Errorf("syft could not read %s from its registry: %s%s", t.Ref, oneLine(err.Error()), withNote(note))
		}
		return data, release.SBOMSourceRegistry, nil
	case KindDir:
		data, err := g.run(ctx, bin, t, "dir:"+t.Ref, nil)
		if err != nil {
			return nil, release.SBOMSourceLockfile, fmt.Errorf("syft on %s: %s", t.Ref, oneLine(err.Error()))
		}
		return data, release.SBOMSourceLockfile, nil
	default: // binary, lockfile
		source := sourceFor(t.Kind)
		data, err := g.run(ctx, bin, t, "file:"+t.Ref, nil)
		if err != nil {
			return nil, source, fmt.Errorf("syft on %s: %s", t.Ref, oneLine(err.Error()))
		}
		return data, source, nil
	}
}

// run runs syft on src. env is added to syft's environment only; it carries
// a registry credential and so is never logged, never part of an error, and
// never on the command line.
func (g *Generator) run(ctx context.Context, bin string, t Target, src string, env []string) ([]byte, error) {
	r := g.Runner
	if r == nil {
		r = execRunner{}
	}
	out, err := r.Run(ctx, bin, syftArgs(t, src), env)
	if err != nil {
		return nil, err
	}
	if err := checkDocument(out); err != nil {
		return nil, err
	}
	return out, nil
}

// syftArgs is the one syft invocation: the source, the pinned output format,
// no logging, and the artifact's name as the document's subject (without
// --source-name syft names a file source by its absolute path, which would
// put this machine's layout into the release's evidence).
func syftArgs(t Target, src string) []string {
	args := []string{"scan", src, "-o", syftOutput, "-q", "--source-name", t.Name}
	if t.Version != "" {
		args = append(args, "--source-version", t.Version)
	}
	if t.Platform != "" && (t.Kind == KindImage || t.Kind == KindRegistry) {
		args = append(args, "--platform", t.Platform)
	}
	return args
}

// checkDocument refuses output that is not the CycloneDX document asked
// for, so a syft that printed a warning to stdout, or ignored the version,
// records "unavailable" rather than bytes no reader can parse.
func checkDocument(out []byte) error {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return fmt.Errorf("syft wrote no SBOM")
	}
	var head struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
	}
	if err := json.Unmarshal(out, &head); err != nil {
		return fmt.Errorf("syft wrote something that is not JSON: %v", err)
	}
	if head.BOMFormat != "CycloneDX" || head.SpecVersion != specVersion {
		return fmt.Errorf("syft wrote %q %q, not a CycloneDX %s document", head.BOMFormat, head.SpecVersion, specVersion)
	}
	return nil
}

func sourceFor(k Kind) string {
	switch k {
	case KindImage:
		return release.SBOMSourceImage
	case KindRegistry:
		return release.SBOMSourceRegistry
	case KindBinary:
		return release.SBOMSourceBinary
	case KindLockfile, KindDir:
		return release.SBOMSourceLockfile
	}
	return ""
}

func okResult(t Target, generator, source string, data []byte, cached bool) Result {
	return Result{
		Target: t,
		Bytes:  data,
		Cached: cached,
		Ref: release.SBOMRef{
			Digest:    Digest(data),
			Format:    Format,
			Generator: generator,
			Source:    source,
			Status:    release.SBOMStatusOK,
		},
	}
}

func unavailable(t Target, source, reason string) Result {
	return Result{Target: t, Ref: release.SBOMRef{Source: source, Status: release.SBOMStatusUnavailable, Reason: reason}}
}

// Digest is the SBOM digest of data, as release.SBOMRef.Digest and the
// machine ledger's SBOM store name it.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the caller names a file of the project it builds
	if err != nil {
		return "", fmt.Errorf("read %s: %v", path, err)
	}
	return Digest(data), nil
}

func withNote(note string) string {
	if note == "" {
		return ""
	}
	return "; " + note
}

// oneLine keeps an error to one line of a reason: syft's stderr can be a
// page, and a release record's reason is read in a table.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			s = l
			break
		}
	}
	const max = 300
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
