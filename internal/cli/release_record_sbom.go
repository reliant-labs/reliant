package cli

// SBOMs for `forge release record` (design doc §1.5, §1.6).
//
// A record names what an outside pipeline deployed, and carries the same
// evidence a forge cut does: an SBOM per workload, set on its
// Artifact.SBOM, with the bytes in this machine's ledger SBOM store. They
// come from one of three places:
//
//   - by default, each --image is described from its registry by digest
//     (Source registry), with the registry credentials the CI already has:
//     the docker credential store `docker login` writes, or syft's own
//     SYFT_REGISTRY_AUTH_* variables. The cut's generator, cache and
//     post-generate hooks do the work.
//   - --sbom <workload>=<path>: an SBOM the pipeline produced itself,
//     CycloneDX or SPDX JSON, recorded verbatim (Source ci-provided).
//   - --sbom-source <dir>: a version-only record names no image, so the
//     source tree's lockfiles are described instead, as the release's own
//     evidence (release.Release.SourceSBOM). It adds no artifact: a
//     release's identity never depends on a flag, so the same version
//     recorded with or without it is the same release.
//
// An env that declares provenance_sbom off skips only the default, the
// generation from each --image. An SBOM the caller supplies (--sbom) or
// explicitly asks for (--sbom-source) is recorded under any policy:
// explicit evidence is never discarded.
//
// WARN-FIRST, as at the cut: an SBOM forge could not produce warns, is
// recorded unavailable with the reason, and the record still succeeds. What
// fails the record is a mistake in the flags themselves (an --sbom for a
// workload the record does not name, a file that is not an SBOM), and that
// is caught before anything is cut.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/pkg/release"
)

// recordSBOMOptions are the SBOM flags of `forge release record`.
type recordSBOMOptions struct {
	// specs is --sbom, each <workload>=<path>.
	specs []string
	// noSBOM is --no-sbom.
	noSBOM bool
	// source is --sbom-source.
	source string
}

// recordSBOMEntry is one artifact's SBOM in the record's result (`--json`
// `sboms`).
type recordSBOMEntry struct {
	// Workload is the artifact's name in the release (the --workload), or
	// "" for the release's source SBOM (--sbom-source). A workload is never
	// named "", so the two cannot be confused.
	Workload string `json:"workload"`
	Digest   string `json:"digest,omitempty"`
	Status   string `json:"status"`
	Source   string `json:"source,omitempty"`
	Format   string `json:"format,omitempty"`
	// Referrer is where the SBOM was attached beside its image (an OCI
	// referrer, `repo@sha256:…`), when it was.
	Referrer string `json:"referrer,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// providedSBOM is one --sbom, read and identified.
type providedSBOM struct {
	path string
	data []byte
	doc  sbom.Document
}

// recordSBOMPlan is the SBOM half of a record, checked before anything is
// cut.
type recordSBOMPlan struct {
	noSBOM   bool
	provided map[string]providedSBOM
	// sourceDir is --sbom-source, absolute.
	sourceDir string
}

// planRecordSBOMs checks the SBOM flags against the record's workloads and
// reads every --sbom. An error here is the caller's mistake, and the record
// is refused before anything is written.
func planRecordSBOMs(o recordSBOMOptions, artifacts map[string]release.Artifact) (recordSBOMPlan, error) {
	plan := recordSBOMPlan{noSBOM: o.noSBOM}
	source := strings.TrimSpace(o.source)
	if o.noSBOM && (len(o.specs) > 0 || source != "") {
		return plan, errors.New("--no-sbom records no SBOM, so it cannot be combined with --sbom or --sbom-source")
	}
	if source != "" {
		if len(artifacts) > 0 {
			return plan, errors.New("--sbom-source describes a version-only record, which names no image; " +
				"with --workload/--image pairs each SBOM comes from its image, or from --sbom <workload>=<path>")
		}
		abs, err := filepath.Abs(source)
		if err != nil {
			return plan, fmt.Errorf("--sbom-source %s: %w", source, err)
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return plan, fmt.Errorf("--sbom-source %s is not a directory", source)
		}
		plan.sourceDir = abs
	}
	for _, spec := range o.specs {
		name, path, ok := strings.Cut(spec, "=")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if !ok || name == "" || path == "" {
			return plan, fmt.Errorf("--sbom %q must be <workload>=<path>", spec)
		}
		if _, known := artifacts[name]; !known {
			return plan, fmt.Errorf("--sbom names workload %q, which this record does not: %s", name, recordWorkloadList(artifacts))
		}
		if _, dup := plan.provided[name]; dup {
			return plan, fmt.Errorf("--sbom names workload %s twice", name)
		}
		p, err := readProvidedSBOM(path)
		if err != nil {
			return plan, fmt.Errorf("--sbom %s=%s: %w", name, path, err)
		}
		if plan.provided == nil {
			plan.provided = map[string]providedSBOM{}
		}
		plan.provided[name] = p
	}
	return plan, nil
}

func recordWorkloadList(artifacts map[string]release.Artifact) string {
	if len(artifacts) == 0 {
		return "it names no workload (pass --workload/--image pairs)"
	}
	return "it names " + strings.Join(sortedArtifactNames(artifacts), ", ")
}

// readProvidedSBOM reads an SBOM a pipeline produced and checks that it is
// one forge can record.
func readProvidedSBOM(path string) (providedSBOM, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return providedSBOM{}, err
	}
	if !fi.Mode().IsRegular() {
		return providedSBOM{}, errors.New("not a file")
	}
	if fi.Size() > sbom.MaxProvidedSize {
		return providedSBOM{}, fmt.Errorf("the file is %d bytes, over the %d MiB an SBOM may be", fi.Size(), sbom.MaxProvidedSize>>20)
	}
	data, err := os.ReadFile(path) //nolint:gosec // a file the caller names on the command line
	if err != nil {
		return providedSBOM{}, err
	}
	doc, err := sbom.Identify(data)
	if err != nil {
		return providedSBOM{}, fmt.Errorf("not an SBOM forge can record: %w", err)
	}
	return providedSBOM{path: path, data: data, doc: doc}, nil
}

// recordSBOMRun is what recordReleaseSBOMs needs about the record.
type recordSBOMRun struct {
	projectDir, project, env, version string
	// createdAt is the release's CreatedAt, the hooks' CutAt.
	createdAt time.Time
	// declared is the env's render when a declaration admitted the record.
	declared *KCLEntities
	plan     recordSBOMPlan
}

// recordReleaseSBOMs sets Artifact.SBOM on every artifact of a record and
// stores the bytes in this machine's ledger. It returns the bytes of each
// SBOM it stored, by artifact, for the post-generate hooks, which run only
// once the record is cut (attachRecordReferrers). It never fails the
// record: an SBOM it could not produce or store is recorded unavailable and
// warned.
//
// EXPLICIT EVIDENCE IS NEVER DISCARDED. An env that declares provenance_sbom
// off turns off what forge does by default, generating an SBOM from each
// --image (and requiring one), not what a caller may hand it: an --sbom file
// is recorded under any policy, exactly as it is with the policy on. Under
// off the workloads named only by --image get no SBOM and no entry, as at
// the cut, which records nothing for them and says nothing.
func recordReleaseSBOMs(ctx context.Context, in recordSBOMRun, artifacts map[string]release.Artifact, warn io.Writer) map[string][]byte {
	if len(artifacts) == 0 {
		return nil
	}
	off := sbomPolicyOff(in.declared)
	names := sortedArtifactNames(artifacts)
	if in.plan.noSBOM {
		if off {
			return nil // the policy already records none, and says nothing
		}
		for _, name := range names {
			setArtifactSBOM(artifacts, name, release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "skipped: --no-sbom"})
		}
		fmt.Fprintf(warn, "warning: --no-sbom: release %s recorded without an SBOM for its %d workload(s); they will show as not scanned\n",
			in.version, len(names))
		return nil
	}

	var (
		store    *ledgerfile.Store
		storeErr error
		opened   bool
	)
	put := func(name string, ref release.SBOMRef, data []byte) bool {
		if !opened {
			store, storeErr = openRecordSBOMStore(in.projectDir, in.project)
			opened = true
		}
		err := storeErr
		if err == nil {
			var digest string
			digest, err = store.PutSBOM(data)
			if err == nil && digest != ref.Digest {
				err = fmt.Errorf("the ledger stored it as %s, not %s", digest, ref.Digest)
			}
		}
		if err != nil {
			setArtifactSBOM(artifacts, name, release.SBOMRef{Status: release.SBOMStatusUnavailable, Source: ref.Source,
				Reason: "could not store the SBOM in the machine ledger: " + err.Error()})
			return false
		}
		setArtifactSBOM(artifacts, name, ref)
		return true
	}

	stored := map[string][]byte{}
	var targets []plannedSBOM
	for _, name := range names {
		art := artifacts[name]
		if p, ok := in.plan.provided[name]; ok {
			ref := release.SBOMRef{Digest: sbom.Digest(p.data), Format: p.doc.Format, Generator: p.doc.Generator,
				Source: release.SBOMSourceCIProvided, Status: release.SBOMStatusOK}
			if put(name, ref, p.data) {
				stored[name] = p.data
			}
			continue
		}
		if off {
			continue // implicit generation is what provenance_sbom off turns off
		}
		digest, ok := art.SharedDigest()
		if !ok {
			continue // a record names shared images only; anything else describes no shipped bytes here
		}
		// The repository, not the workload, names the document's subject,
		// and an image SBOM carries no release version, as at the cut: the
		// same digest, cut by forge or recorded, is one SBOM (and one
		// cache entry).
		subject := art.URI
		if subject == "" {
			subject = name
		}
		targets = append(targets, plannedSBOM{name: name,
			target: sbom.Target{Name: subject, Kind: sbom.KindRegistry, Ref: subject + "@" + digest, Subject: digest}})
	}

	if len(targets) > 0 {
		gen := newRecordSBOMGenerator(in)
		list := make([]sbom.Target, len(targets))
		for i, t := range targets {
			list[i] = t.target
		}
		for i, r := range gen.Generate(ctx, list) {
			name := targets[i].name
			if !r.Ref.HasSBOM() {
				setArtifactSBOM(artifacts, name, r.Ref)
				continue
			}
			if put(name, r.Ref, r.Bytes) {
				stored[name] = r.Bytes
			}
		}
	}

	if off && len(in.plan.provided) > 0 {
		fmt.Fprintf(warn, "note: env %s declares provenance_sbom off, so forge generated no SBOM; the %d SBOM(s) passed with --sbom are recorded\n",
			in.env, len(in.plan.provided))
	}

	for _, name := range names {
		if ref := artifacts[name].SBOM; ref != nil && !ref.HasSBOM() {
			fmt.Fprintf(warn, "warning: release %s recorded without an SBOM for %s: %s\n", in.version, name, ref.Reason)
		}
	}
	return stored
}

// newRecordSBOMGenerator is the cut's generator, cache and registry
// credential path (the env's control-plane identity for its platform
// registry, the docker store elsewhere), for a record.
func newRecordSBOMGenerator(in recordSBOMRun) *sbom.Generator {
	gen := &sbom.Generator{
		Tool: sbomTool, Runner: sbomRunner, RegistryAuth: sbomRegistryAuth(in.projectDir, in.env, in.declared),
		Concurrency: sbom.DefaultConcurrency, Timeout: sbom.DefaultTimeout,
	}
	if dir := sbomCacheDir(); dir != "" {
		gen.Cache = &sbom.Cache{Dir: dir}
	}
	return gen
}

// recordSourceSBOM describes --sbom-source's tree for a version-only record
// and stores the bytes in this machine's ledger: the release's SourceSBOM.
// Like every record SBOM it never fails the record; nil without
// --sbom-source.
//
// It runs whatever the env's provenance_sbom policy: --sbom-source is the
// caller explicitly asking for this SBOM, and explicit evidence is never
// discarded (recordReleaseSBOMs).
//
// The post-generate hooks do not run for it: they act on an artifact (a
// referrer is attached to an image), and a source SBOM has none.
func recordSourceSBOM(ctx context.Context, in recordSBOMRun, warn io.Writer) *release.SBOMRef {
	if in.plan.sourceDir == "" {
		return nil
	}
	// A tree has no one digest, so the scan is never cached (the cache
	// keys on a subject digest); the release version is the tree's
	// version, which is what the document should say.
	r := newRecordSBOMGenerator(in).Generate(ctx, []sbom.Target{{
		Name: in.project, Kind: sbom.KindDir, Ref: in.plan.sourceDir, Version: in.version}})[0]
	ref := r.Ref
	if ref.HasSBOM() {
		if err := putRecordSBOM(in, ref, r.Bytes); err != nil {
			ref = release.SBOMRef{Status: release.SBOMStatusUnavailable, Source: ref.Source,
				Reason: "could not store the SBOM in the machine ledger: " + err.Error()}
		}
	}
	if !ref.HasSBOM() {
		fmt.Fprintf(warn, "warning: release %s recorded without an SBOM of its source %s: %s\n", in.version, in.plan.sourceDir, ref.Reason)
	}
	return &ref
}

// putRecordSBOM stores data in the record project's machine ledger and
// checks it was stored under ref's digest.
func putRecordSBOM(in recordSBOMRun, ref release.SBOMRef, data []byte) error {
	store, err := openRecordSBOMStore(in.projectDir, in.project)
	if err != nil {
		return err
	}
	digest, err := store.PutSBOM(data)
	if err == nil && digest != ref.Digest {
		err = fmt.Errorf("the ledger stored it as %s, not %s", digest, ref.Digest)
	}
	return err
}

// attachRecordReferrers runs the post-generate hooks (the OCI referrer
// attach beside each image, a scan) over the SBOMs a record stored, once the
// record is cut and promoted, and backfills the referrers they add into the
// recorded release. A refused record never reaches it, so nothing is written
// beside an image for a release that was not recorded; a record whose
// promotion failed attaches nothing either, and its retry (an idempotent cut,
// a no-op promotion) attaches then.
//
// The hooks act on what the release RECORDS: an artifact whose recorded SBOM
// is another one (a retry after a syft upgrade; the first SBOM recorded
// stands) keeps it and is not attached again, nor is one whose recorded SBOM
// already names a referrer.
//
// The referrers are backfilled through the ledger's own fill-never-replace
// path (release.BackfillSBOMs, which a re-cut of the same content runs). A
// control plane records no SBOM refs yet, so on a hosted ledger the
// referrers are attached and listed in the result, and not persisted.
func attachRecordReferrers(ctx context.Context, target recordLedger, in recordSBOMRun, artifacts map[string]release.Artifact, stored map[string][]byte, warn io.Writer) {
	if len(sbomPostGenerate) == 0 || len(stored) == 0 {
		return
	}
	recorded, err := target.ledger.Releases.Get(ctx, in.version)
	if err != nil {
		fmt.Fprintf(warn, "warning: release %s is recorded, but its SBOMs were not attached beside the images: read it back: %v\n", in.version, err)
		return
	}
	run := map[string][]byte{}
	for name, data := range stored {
		if recorded != nil {
			if ref := recorded.Artifacts[name].SBOM; ref.HasSBOM() && (ref.Digest != artifacts[name].SBOM.Digest || ref.Referrer != "") {
				setArtifactSBOM(artifacts, name, *ref)
				continue
			}
		}
		run[name] = data
	}
	runRecordSBOMHooks(ctx, in, artifacts, run, warn)

	attached := 0
	for name := range run {
		if artifacts[name].SBOM.Referrer != "" {
			attached++
		}
	}
	if attached == 0 {
		return
	}
	if target.ledger.Hosted || recorded == nil {
		fmt.Fprintf(warn, "note: release %s's SBOMs are attached beside %d image(s); the control plane does not record SBOM references yet, so the referrers are listed here only\n",
			in.version, attached)
		return
	}
	filled := *recorded
	filled.Artifacts = make(map[string]release.Artifact, len(recorded.Artifacts))
	for n, a := range recorded.Artifacts {
		if _, ok := run[n]; ok {
			a.SBOM = artifacts[n].SBOM
		}
		filled.Artifacts[n] = a
	}
	if _, err := target.ledger.Releases.Cut(ctx, filled); err != nil {
		fmt.Fprintf(warn, "warning: release %s's SBOMs are attached beside the images, but the referrers were not recorded: %v\n", in.version, err)
	}
}

// runRecordSBOMHooks runs the cut's post-generate hooks (attach as an OCI
// referrer, scan) over each SBOM the record stored, generated or provided,
// at most 4 artifacts at once. The hooks see Name as the workload; an
// image's repository is Artifact.URI.
func runRecordSBOMHooks(ctx context.Context, in recordSBOMRun, artifacts map[string]release.Artifact, stored map[string][]byte, warn io.Writer) {
	if len(sbomPostGenerate) == 0 || len(stored) == 0 {
		return
	}
	inputs := make([]sbomGenerated, 0, len(stored))
	for _, name := range sortedArtifactNames(artifacts) {
		if data, ok := stored[name]; ok {
			// Built: an image a record names by --image is one the CI
			// recording it pushed, so a referrer beside it is the
			// project's to write (sbomSubjectPushedByProject).
			art := artifacts[name]
			_, image := art.SharedDigest()
			inputs = append(inputs, sbomGenerated{ProjectDir: in.projectDir, Env: in.env, Version: in.version,
				CutAt: in.createdAt, Name: name, Built: image, Artifact: art, Bytes: data, Entities: in.declared})
		}
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 4)
	)
	for _, g := range inputs {
		wg.Add(1)
		go func(g sbomGenerated) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ref := runSBOMHooks(ctx, g, warn, &mu)
			mu.Lock()
			setArtifactSBOM(artifacts, g.Name, ref)
			mu.Unlock()
		}(g)
	}
	wg.Wait()
}

// openRecordSBOMStore opens this machine's ledger SBOM store for the
// record's project. It is keyed by the project the record is filed under,
// not by forge.yaml, because a record needs no forge.yaml: an env document
// can name the project alone.
func openRecordSBOMStore(projectDir, project string) (*ledgerfile.Store, error) {
	home, err := ledgerHome()
	if err != nil {
		return nil, err
	}
	id, err := ledgerfile.ProjectID(project, originURL(projectDir))
	if err != nil {
		return nil, err
	}
	return ledgerfile.Open(home, id)
}

// sourceSBOMEntry is the `sboms` entry of a release's source SBOM:
// workload "".
func sourceSBOMEntry(ref *release.SBOMRef) []recordSBOMEntry {
	if ref == nil {
		return nil
	}
	return []recordSBOMEntry{{Digest: ref.Digest, Status: ref.Status, Source: ref.Source, Format: ref.Format, Reason: ref.Reason}}
}

// recordSBOMEntries lists every artifact's SBOM ref, by name.
func recordSBOMEntries(artifacts map[string]release.Artifact) []recordSBOMEntry {
	var out []recordSBOMEntry
	for _, name := range sortedArtifactNames(artifacts) {
		ref := artifacts[name].SBOM
		if ref == nil {
			continue
		}
		out = append(out, recordSBOMEntry{Workload: name, Digest: ref.Digest, Status: ref.Status,
			Source: ref.Source, Format: ref.Format, Referrer: ref.Referrer, Reason: ref.Reason})
	}
	return out
}

// printRecordSBOMs is the text output's SBOM lines.
func printRecordSBOMs(w io.Writer, entries []recordSBOMEntry) {
	for _, e := range entries {
		what := e.Workload
		if what == "" {
			what = "(source)"
		}
		if e.Digest != "" {
			fmt.Fprintf(w, "  sbom     %-8s %s (%s, %s)\n", what, e.Digest, e.Format, e.Source)
			continue
		}
		fmt.Fprintf(w, "  sbom     %-8s none: %s\n", what, e.Reason)
	}
}
