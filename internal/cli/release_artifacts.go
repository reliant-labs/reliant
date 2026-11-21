package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/pkg/release"
)

// Non-OCI artifact harvesting.
//
// harvestReleaseArtifacts (release.go) projects the container images a build
// pushed. This file does the same job for the three other things a release
// ships — npm packages, Go modules, and published files — and it holds to the
// same rule, which is the load-bearing property of the whole model: THE LEDGER
// IS A PROJECTION OF BUILD STATE, NOT A PARALLEL CAPTURE MECHANISM. Every
// coordinate below is read out of something the build or the repo already
// produced. Nothing here builds, publishes, or mutates anything.
//
// Why this matters concretely: forge v0.1.12 tagged `web-runtime/v0.3.1` and
// never published it to npm. The ledger could not say "this release also
// contains an npm package at this version with this integrity hash", so
// nothing compared the two, and the gap surfaced as a scaffolded project
// failing to install. Naming the artifact is the prerequisite for verifying it.
//
// ABSENCE IS NOT FAILURE. A project with no npm package, no nested Go module
// and no build-only binaries harvests nothing from this file, and that is a
// normal release, not an error. Every function here is best-effort: a missing
// tool, an unreadable manifest or a failed subprocess yields no artifact and
// no error. Only a release with NOTHING AT ALL is a problem, and that check
// already lives in writeReleaseLedger.

// releaseScanSkipDirs are directory names never descended into when scanning a
// project for package manifests. They are either dependency trees (node_modules,
// vendor) or build output (dist, build, out, .next, bin) — a manifest found
// inside one describes something the project consumes or emits a copy of, not
// something it releases. Skipping them also keeps the scan bounded on a large
// monorepo, where node_modules alone dwarfs the real source tree.
var releaseScanSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"bin":          true,
	".next":        true,
	".git":         true,
	".forge":       true,
	"testdata":     true,
}

// scanForManifests walks projectDir and returns the directories containing a
// file named manifest ("package.json", "go.mod"), skipping releaseScanSkipDirs.
// The project root itself is included when it carries the manifest.
//
// Returns paths to the CONTAINING DIRECTORY rather than the file, because every
// caller's next step is to run a tool in that directory.
func scanForManifests(projectDir, manifest string) []string {
	var dirs []string
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is skipped, not fatal: harvesting is
			// best-effort and a permissions error somewhere in the tree must
			// never fail a build that already succeeded.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Never skip the root itself, even if its basename happens to
			// collide with a skip entry (a checkout literally named "build").
			if path != projectDir && releaseScanSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == manifest {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	return dirs
}

// npmManifest is the subset of package.json that decides whether a package is
// releasable and what it is called.
type npmManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Private is npm's own opt-out: `npm publish` REFUSES a private package.
	// It is therefore the authoritative declaration of "this is not released",
	// which is why no new forge-specific field is needed to decide what to
	// harvest. Every scaffolded forge frontend sets it, so they are excluded
	// for free — correctly, since an app bundle is not a published package.
	Private bool `json:"private"`
}

// npmPackEntry is one element of `npm pack --json`'s output array.
type npmPackEntry struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

// harvestNPMArtifacts records every publishable npm package in the project as
// an release.KindNPM artifact, keyed by package name.
//
// SOURCE: `npm pack --dry-run --ignore-scripts --json`, run in each package
// directory. The `integrity` it reports is the sha512 of the tarball npm would
// publish — byte-for-byte the string the registry serves back as
// `dist.integrity`, which is what makes it verifiable later.
//
// WHY npm pack AND NOT THE REGISTRY. Reading the registry's metadata would
// describe what is ALREADY published, and at the moment a release is cut that
// is either nothing (404) or the PREVIOUS version — so the ledger would record
// a hash for bytes this release did not produce. That is not a hypothetical
// mis-step: it is precisely the v0.1.12 failure shape, where the tag existed
// and the publish did not, and a registry read would have cheerfully recorded
// v0.3.0's hash under v0.3.1. npm pack describes the bytes ON DISK RIGHT NOW,
// which is what "the artifacts of this build" means. (package-lock.json, the
// third candidate source, describes the package's DEPENDENCIES, not itself.)
//
// WHY --ignore-scripts IS LOAD-BEARING. Without it, npm runs the `prepare`
// lifecycle script, which for forge's own web-runtime is a full rebuild. That
// would make the ledger a capture mechanism that PRODUCES the artifact it
// claims to be observing — the exact inversion this model forbids, and a way
// for a release to record bytes that were never tested. With it, pack reads
// the dist/ the build already emitted. Verified on web-runtime: both spellings
// yield an identical integrity hash, and --ignore-scripts is ~7x faster.
//
// Shelling out is consistent with the OCI path, which reads digests via
// `docker buildx imagetools inspect`; npm is the tool that owns the tarball
// format, and reimplementing its packing rules would be a second source of
// truth that silently disagrees.
//
// Unauthenticated by construction: `npm pack` on a local directory touches no
// registry and needs no token. Best-effort throughout — no npm on PATH, a
// malformed manifest, or a non-zero exit from npm yields no artifact.
func harvestNPMArtifacts(ctx context.Context, projectDir string) map[string]release.Artifact {
	out := map[string]release.Artifact{}
	if _, err := exec.LookPath("npm"); err != nil {
		return out
	}
	for _, dir := range scanForManifests(projectDir, "package.json") {
		man, ok := readNPMManifest(filepath.Join(dir, "package.json"))
		if !ok {
			continue
		}
		entry, ok := npmPackMetadata(ctx, dir)
		if !ok {
			continue
		}
		// npm's own report wins over the manifest read: if the two ever
		// disagree, what npm would actually publish is the truth.
		name, version := entry.Name, entry.Version
		if name == "" {
			name = man.Name
		}
		if version == "" {
			version = man.Version
		}
		if name == "" || version == "" {
			continue
		}
		out[name] = release.Artifact{
			Kind:      release.KindNPM,
			Mode:      release.ModeShared,
			Version:   version,
			Integrity: entry.Integrity,
		}
	}
	return out
}

// readNPMManifest parses a package.json and reports whether it describes a
// package worth recording in a release: named, versioned, and not private.
func readNPMManifest(path string) (npmManifest, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from a walk of the project tree
	if err != nil {
		return npmManifest{}, false
	}
	var man npmManifest
	if err := json.Unmarshal(data, &man); err != nil {
		return npmManifest{}, false
	}
	// A manifest with no name/version is not a package — forge's own
	// web-runtime/interceptors/package.json is exactly this: a resolver shim
	// carrying only `main`/`types`, shipped INSIDE the parent's tarball.
	// Recording it would name an artifact that does not independently exist.
	if man.Name == "" || man.Version == "" || man.Private {
		return npmManifest{}, false
	}
	return man, true
}

// npmPackMetadata runs `npm pack --dry-run --ignore-scripts --json` in dir and
// returns the first (only) entry. --dry-run means no tarball is written, so
// this leaves the tree exactly as it found it.
func npmPackMetadata(ctx context.Context, dir string) (npmPackEntry, bool) {
	cmd := exec.CommandContext(ctx, "npm", "pack", "--dry-run", "--ignore-scripts", "--json")
	cmd.Dir = dir
	// stderr is deliberately dropped: npm narrates the pack contents there,
	// and a release cut should not spray a file listing across the build log.
	stdout, err := cmd.Output()
	if err != nil {
		return npmPackEntry{}, false
	}
	var entries []npmPackEntry
	if err := json.Unmarshal(stdout, &entries); err != nil || len(entries) == 0 {
		return npmPackEntry{}, false
	}
	// A pack with no integrity hash is not content-addressed, and the ledger
	// records only what can later be verified (the same rule that makes the
	// OCI path skip a digestless image).
	if entries[0].Integrity == "" {
		return npmPackEntry{}, false
	}
	return entries[0], true
}

// harvestGoModuleArtifacts records every NESTED Go module in the project whose
// version the root module pins, as an release.KindGoModule artifact keyed by
// module path.
//
// SOURCE: the repo's own go.mod and go.sum. The root module's `require` line
// gives the version a consumer will resolve, and go.sum's `<path> <version>
// h1:…` line gives the hash that version must verify against. Both files are
// build state in the most literal sense — the build that just ran compiled
// against exactly these — so no download, no proxy read, and no credentials.
//
// WHY NESTED MODULES, AND WHY THE ROOT'S go.mod IS THE VERSION SOURCE. A
// submodule's own go.mod does not state its version; nothing in a module
// declares that. The version only exists where something DEPENDS on it, which
// for forge/pkg is the root go.mod. This is the same pair of facts
// scripts/release-forge.sh exists to keep honest: it resolves the hashes into
// go.sum before the tag is pushed, because an in-workspace build passes
// without them (go.work resolves pkg from disk) and the gap only surfaces for
// a consumer, after release. Recording the pair in the ledger is what lets a
// verifier catch that drift instead of a stranger's `go mod download`.
//
// The ROOT module itself is deliberately not recorded: its version IS the
// release label, and it has no self-referential go.sum entry to verify against.
func harvestGoModuleArtifacts(projectDir string) map[string]release.Artifact {
	out := map[string]release.Artifact{}

	rootData, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return out
	}
	rootPath := modfile.ModulePath(rootData)
	required := requiredModuleVersions(rootData)
	if len(required) == 0 {
		return out
	}
	sums := goSumHashes(filepath.Join(projectDir, "go.sum"))

	for _, dir := range scanForManifests(projectDir, "go.mod") {
		if dir == projectDir {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // path comes from a walk of the project tree
		if rerr != nil {
			continue
		}
		modPath := modfile.ModulePath(data)
		if modPath == "" || modPath == rootPath {
			continue
		}
		version, ok := required[modPath]
		if !ok {
			// A nested module the root does not depend on has no version to
			// record. Releasing it would need a coordinate that exists
			// nowhere in this repo.
			continue
		}
		// No go.sum hash means no way to verify the bytes — record nothing,
		// exactly as the OCI path skips a digestless image.
		integrity, ok := sums[modPath+" "+version]
		if !ok {
			continue
		}
		out[modPath] = release.Artifact{
			Kind:      release.KindGoModule,
			Mode:      release.ModeShared,
			Version:   version,
			Integrity: integrity,
		}
	}
	return out
}

// requiredModuleVersions returns module path → version for every require in a
// parsed go.mod, including those inside a require block.
func requiredModuleVersions(data []byte) map[string]string {
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(f.Require))
	for _, r := range f.Require {
		if r == nil || r.Mod.Path == "" {
			continue
		}
		out[r.Mod.Path] = r.Mod.Version
	}
	return out
}

// goSumHashes indexes a go.sum by "<path> <version>" → "h1:…".
//
// Only the MODULE ZIP hash is indexed. go.sum carries a second line per module
// ("<path> <version>/go.mod h1:…") hashing the go.mod file alone; a consumer
// needs both, but the artifact's integrity is the hash of its CONTENT, and
// conflating the two would record a hash that verifies the wrong bytes. The
// "/go.mod" suffix on the version field is what distinguishes them, so keying
// on the exact "<path> <version>" pair excludes it without a special case.
func goSumHashes(path string) map[string]string {
	data, err := os.ReadFile(path) //nolint:gosec // path is the project's own go.sum
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "h1:") {
			continue
		}
		out[fields[0]+" "+fields[1]] = fields[2]
	}
	return out
}

// harvestFileArtifacts records the binaries produced by build-only service
// variants as release.KindFile artifacts, keyed by the binary's output name.
//
// SOURCE: the KCL entities the build already rendered, plus the binaries on
// disk in outputDir. A build-only service is forge's declared shape for "an
// artifact this project ships but never schedules" — schema.k describes it as
// a CLI binary shipped in a release artifact — and each variant's output name
// is fully determined by the declaration (`output_name`, else
// `<service>-<variant>`). So this enumerates EXACTLY the declared outputs
// rather than hashing whatever happens to be sitting in bin/, which would
// double-count the project binaries that are baked into an OCI image.
//
// Integrity is "sha256:<hex>" over the file's bytes — the same canonical
// spelling the OCI path uses, so a human reading the ledger sees one hash
// format rather than two.
//
// URI IS DELIBERATELY EMPTY, AND THAT IS A KNOWN GAP. A file artifact's other
// half is WHERE it was published, and no forge build state records that: forge
// builds these binaries but never uploads them, and nothing in KCL declares a
// destination. The motivating external case (reliant's Electron DMGs) is
// published by electron-builder inside reliant's own CI, which forge never
// observes — so a URL here could only come from scraping a workflow file or
// from a new declaration, and inventing either would make the ledger assert
// something it did not derive. Naming the artifact and its hash is strictly
// better than not naming it at all; closing the gap needs a publish
// destination declared in KCL, which is a separate change.
func harvestFileArtifacts(projectDir, outputDir string, entities *KCLEntities) map[string]release.Artifact {
	out := map[string]release.Artifact{}
	if entities == nil {
		return out
	}
	if outputDir == "" {
		outputDir = "bin"
	}
	if !filepath.IsAbs(outputDir) {
		// `forge build` runs with the project root as its cwd, so a relative
		// -o resolves against it.
		outputDir = filepath.Join(projectDir, outputDir)
	}

	for _, svc := range entities.WorkloadsOn(RuntimeBuildOnly) {
		for _, v := range svc.Runtime.BuildOnly.BuildVariants {
			name := v.OutputName
			if name == "" {
				name = svc.Name + "-" + v.Name
			}
			sum, ok := sha256File(filepath.Join(outputDir, name))
			if !ok {
				// The variant declared a binary this build did not produce
				// (a --target run that skipped it, or a failed variant). A
				// release records only bytes that exist.
				continue
			}
			out[name] = release.Artifact{
				Kind:      release.KindFile,
				Mode:      release.ModeShared,
				Version:   v.Name,
				Integrity: sum,
			}
		}
	}
	return out
}

// sha256File returns "sha256:<hex>" for a file's contents, or ("", false) when
// it cannot be read.
func sha256File(path string) (string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path is composed from the project's own KCL declarations
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), true
}

// mergeReleaseArtifacts copies src into dst WITHOUT overwriting an existing
// key, and reports how many entries it added.
//
// The no-overwrite rule makes the OCI path structurally untouchable: callers
// merge images FIRST, so a package or file that happens to share a name with
// an image can never displace the digest an env deploys. That is belt-and-
// braces on top of SharedDigest's kind guard — that guard already stops a
// non-OCI artifact from being read AS a digest, and this stops one from
// evicting a real digest in the first place.
func mergeReleaseArtifacts(dst, src map[string]release.Artifact) int {
	added := 0
	for name, art := range src {
		if _, exists := dst[name]; exists {
			continue
		}
		dst[name] = art
		added++
	}
	return added
}
