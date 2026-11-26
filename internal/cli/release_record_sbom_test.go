package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/sbomoci/ocitest"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	recordCycloneDX = `{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"tools":{"components":[{"name":"trivy","version":"0.50.0"}]}},"components":[]}`
	recordSPDX      = `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"creators":["Tool: syft-1.57.2"]},"packages":[]}`
)

// sbomRecordTest is one record test's surroundings: a fake control plane, a
// fake syft, a private ledger home, and the options for two workloads.
type sbomRecordTest struct {
	cp   *fakeRecordCP
	syft *cutSyft
	home string
	sc   recordScope
}

func newSBOMRecordTest(t *testing.T) *sbomRecordTest {
	t.Helper()
	rt := &sbomRecordTest{cp: newFakeRecordCP(), syft: &cutSyft{}, home: t.TempDir()}
	useRecordLedger(t, hostedRecordTarget(rt.cp))
	useFakeSyft(t, rt.syft, "")
	useLedgerHome(t, rt.home)
	rt.sc = recordScopeFor(t, githubEnv)
	return rt
}

func (rt *sbomRecordTest) store(t *testing.T) interface {
	GetSBOM(string) ([]byte, error)
} {
	t.Helper()
	s, err := openRecordSBOMStore(rt.sc.projectDir, rt.sc.project)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func twoWorkloads(o releaseRecordOptions) releaseRecordOptions {
	o.env, o.version = "prod", "v1.4.0"
	o.workloads = []string{"api", "web"}
	o.images = []string{"ghcr.io/acme/api@" + recordDigestA, "ghcr.io/acme/web@" + recordDigestB}
	return o
}

func writeSBOMFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sbomEntry(t *testing.T, res releaseRecordResult, workload string) recordSBOMEntry {
	t.Helper()
	for _, e := range res.SBOMs {
		if e.Workload == workload {
			return e
		}
	}
	t.Fatalf("no SBOM entry for %s in %+v", workload, res.SBOMs)
	return recordSBOMEntry{}
}

// The zero-flag path: each --image is described from its registry by digest,
// under the repository's name, and the bytes land in the machine ledger.
func TestReleaseRecordSBOM_GeneratedFromEachImageByDefault(t *testing.T) {
	rt := newSBOMRecordTest(t)
	res, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{}), rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	calls := rt.syft.calls()
	sort.Strings(calls)
	if got, want := strings.Join(calls, " "),
		"registry:ghcr.io/acme/api@"+recordDigestA+" registry:ghcr.io/acme/web@"+recordDigestB; got != want {
		t.Errorf("syft read %q, want %q", got, want)
	}
	for _, w := range []string{"api", "web"} {
		e := sbomEntry(t, res, w)
		if e.Status != release.SBOMStatusOK || e.Source != release.SBOMSourceRegistry || e.Format != sbom.Format || e.Reason != "" {
			t.Errorf("%s: %+v", w, e)
		}
		data, err := rt.store(t).GetSBOM(e.Digest)
		if err != nil || !strings.Contains(string(data), "ghcr.io/acme/"+w) {
			t.Errorf("%s: the machine ledger does not hold SBOM %s: %v", w, e.Digest, err)
		}
	}
	if len(rt.cp.called("CutRelease")) != 1 {
		t.Error("the record was not cut")
	}
}

// A registry read that fails warns and records unavailable; the record
// itself succeeds.
func TestReleaseRecordSBOM_FailureWarnsAndStillRecords(t *testing.T) {
	rt := newSBOMRecordTest(t)
	rt.syft.fail = map[string]error{"registry": errors.New("GET https://ghcr.io/v2/: 401 Unauthorized")}
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{}), rt.sc, &warn)
	if err != nil {
		t.Fatalf("a failed SBOM failed the record: %v", err)
	}
	if !res.ReleaseCreated || !res.Promoted {
		t.Errorf("record: %+v", res)
	}
	e := sbomEntry(t, res, "api")
	if e.Status != release.SBOMStatusUnavailable || e.Digest != "" || !strings.Contains(e.Reason, "401") {
		t.Errorf("api: %+v", e)
	}
	if !strings.Contains(warn.String(), "release v1.4.0 recorded without an SBOM for api: ") {
		t.Errorf("warning: %q", warn.String())
	}
}

// --sbom records the pipeline's own document verbatim, CycloneDX or SPDX,
// with the format it states and the digest of the bytes as given; syft runs
// only for the workload with none.
func TestReleaseRecordSBOM_ProvidedCycloneDXAndSPDX(t *testing.T) {
	rt := newSBOMRecordTest(t)
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	spdx := writeSBOMFile(t, "worker.spdx.json", recordSPDX)
	o := twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{specs: []string{"api=" + cdx, "worker=" + spdx}}})
	o.workloads = append(o.workloads, "worker")
	o.images = append(o.images, "ghcr.io/acme/worker@"+sha("w"))

	res, err := recordReleaseIn(context.Background(), o, rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	for w, want := range map[string]struct{ body, format string }{
		"api":    {recordCycloneDX, "cyclonedx-json@1.5"},
		"worker": {recordSPDX, "spdx-json@2.3"},
	} {
		e := sbomEntry(t, res, w)
		if e.Status != release.SBOMStatusOK || e.Source != release.SBOMSourceCIProvided || e.Format != want.format ||
			e.Digest != sbom.Digest([]byte(want.body)) {
			t.Errorf("%s: %+v", w, e)
		}
		if data, err := rt.store(t).GetSBOM(e.Digest); err != nil || string(data) != want.body {
			t.Errorf("%s: stored %q, %v; want the file's bytes", w, data, err)
		}
	}
	if e := sbomEntry(t, res, "web"); e.Source != release.SBOMSourceRegistry {
		t.Errorf("web, with no --sbom, was not generated: %+v", e)
	}
	if calls := rt.syft.calls(); len(calls) != 1 || !strings.Contains(calls[0], "ghcr.io/acme/web") {
		t.Errorf("syft ran for %v, want only web", calls)
	}
}

// On a control plane that keeps SBOMs, a record uploads each workload's SBOM
// with its cut, as a forge cut does: the bytes recorded on this machine (a
// pipeline's --sbom verbatim, a generated one), under the record's project,
// version and workload. One that does not keep them is sent none.
func TestReleaseRecordSBOM_UploadsToAControlPlaneThatKeepsThem(t *testing.T) {
	rt := newSBOMRecordTest(t)
	rt.cp.vulnFeatures = []string{hostedVulnFeatureSBOMUpload}
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	o := twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{specs: []string{"api=" + cdx}}})
	res, err := recordReleaseIn(context.Background(), o, rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	uploads := rt.cp.called("RecordReleaseSBOM")
	if len(uploads) != 2 {
		t.Fatalf("%d uploads, want one per workload: %+v", len(uploads), uploads)
	}
	got := map[string]map[string]any{}
	for _, u := range uploads {
		got[u["artifactName"].(string)] = u
	}
	for _, w := range []string{"api", "web"} {
		u, e := got[w], sbomEntry(t, res, w)
		if u == nil {
			t.Fatalf("%s's SBOM was not uploaded: %+v", w, uploads)
		}
		if u["version"] != "v1.4.0" || u["project"] != "shop" || u["encoding"] != sbomEncodingGzip || u["format"] != e.Format || u["source"] != e.Source {
			t.Errorf("%s upload = %+v, want v1.4.0 of shop as recorded (%+v)", w, u, e)
		}
	}
	gz, _ := base64.StdEncoding.DecodeString(got["api"]["sbom"].(string))
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(zr); string(data) != recordCycloneDX {
		t.Errorf("api uploaded %q, want the pipeline's file verbatim", data)
	}
	if cuts := rt.cp.called("CutRelease"); len(cuts) != 1 {
		t.Errorf("%d cuts", len(cuts))
	}

	// A control plane that does not keep SBOMs is sent none.
	rt = newSBOMRecordTest(t)
	if _, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{}), rt.sc, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if n := len(rt.cp.called("RecordReleaseSBOM")); n != 0 {
		t.Errorf("%d uploads to a control plane without vuln-sbom-upload", n)
	}
}

// Mistakes in the flags are refused before anything is cut.
func TestReleaseRecordSBOM_FlagMistakesRefuseTheRecord(t *testing.T) {
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	notSBOM := writeSBOMFile(t, "package.json", `{"name":"app"}`)
	for name, tc := range map[string]struct {
		sbom recordSBOMOptions
		want string
	}{
		"unknown workload": {recordSBOMOptions{specs: []string{"apii=" + cdx}}, `workload "apii", which this record does not: it names api, web`},
		"twice":            {recordSBOMOptions{specs: []string{"api=" + cdx, "api=" + cdx}}, "twice"},
		"no path":          {recordSBOMOptions{specs: []string{"api"}}, "<workload>=<path>"},
		"missing file":     {recordSBOMOptions{specs: []string{"api=" + cdx + ".nope"}}, "api.cdx.json.nope"}, // the OS words the not-found error itself
		"not an SBOM":      {recordSBOMOptions{specs: []string{"api=" + notSBOM}}, "not an SBOM forge can record"},
		"no-sbom and sbom": {recordSBOMOptions{noSBOM: true, specs: []string{"api=" + cdx}}, "cannot be combined"},
		"source w/ images": {recordSBOMOptions{source: t.TempDir()}, "version-only"},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newSBOMRecordTest(t)
			_, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{sbom: tc.sbom}), rt.sc, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want one saying %q", err, tc.want)
			}
			if n := len(rt.cp.called("CutRelease")); n != 0 {
				t.Errorf("a refused record cut %d release(s)", n)
			}
		})
	}
}

// --no-sbom records every workload as skipped, says so, and runs no syft.
func TestReleaseRecordSBOM_NoSBOM(t *testing.T) {
	rt := newSBOMRecordTest(t)
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{noSBOM: true}}), rt.sc, &warn)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	for _, w := range []string{"api", "web"} {
		if e := sbomEntry(t, res, w); e.Status != release.SBOMStatusUnavailable || e.Reason != "skipped: --no-sbom" {
			t.Errorf("%s: %+v", w, e)
		}
	}
	if n := len(rt.syft.calls()); n != 0 {
		t.Errorf("syft ran %d time(s) under --no-sbom", n)
	}
	if !strings.Contains(warn.String(), "--no-sbom") {
		t.Errorf("--no-sbom did not warn: %q", warn.String())
	}
}

// --sbom-source on a version-only record: a directory scan of the tree,
// recorded as the release's source SBOM. It adds no artifact, so the same
// version recorded with or without the flag is the same release, and a
// record without it first is filled in, never replaced, by one with it.
func TestReleaseRecordSBOM_SourceTreeOfAVersionOnlyRecord(t *testing.T) {
	rt := newSBOMRecordTest(t)
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(projectDir, "services", "api")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ledgerSelectionEnv, string(ledgerSelectMachine))
	useRecordLedger(t, defaultRecordLedger)
	sc := recordScope{projectDir: projectDir, project: "shop", getenv: func(string) string { return "" }}
	plain := releaseRecordOptions{env: "prod", version: "v1.4.0", commit: recordCommitA}
	withSource := plain
	withSource.sbom = recordSBOMOptions{source: tree}
	recorded := func() *release.Release {
		t.Helper()
		l, err := machineLedger(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := l.Releases.Get(context.Background(), "v1.4.0")
		if err != nil || rel == nil {
			t.Fatalf("release: %v", err)
		}
		return rel
	}

	first, err := recordReleaseIn(context.Background(), plain, sc, &bytes.Buffer{})
	if err != nil || !first.ReleaseCreated || len(first.SBOMs) != 0 {
		t.Fatalf("plain record: %+v %v", first, err)
	}
	if rel := recorded(); rel.SourceSBOM != nil || len(rel.Artifacts) != 0 {
		t.Fatalf("plain record carries %+v / %v", rel.SourceSBOM, rel.Artifacts)
	}

	res, err := recordReleaseIn(context.Background(), withSource, sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("the same version with --sbom-source is a conflict: %v", err)
	}
	if res.ReleaseCreated {
		t.Error("--sbom-source made a different release")
	}
	if len(res.SBOMs) != 1 {
		t.Fatalf("sboms = %+v, want the source entry", res.SBOMs)
	}
	e := res.SBOMs[0]
	if e.Workload != "" || e.Status != release.SBOMStatusOK || e.Source != release.SBOMSourceLockfile || e.Format != sbom.Format {
		t.Errorf("source entry: %+v", e)
	}
	if calls := rt.syft.calls(); len(calls) != 1 || calls[0] != "dir:"+tree {
		t.Errorf("syft read %v, want a dir scan of %s", calls, tree)
	}
	rel := recorded()
	if rel.SourceSBOM == nil || rel.SourceSBOM.Digest != e.Digest || len(rel.Artifacts) != 0 {
		t.Fatalf("recorded source SBOM %+v, artifacts %v; want the SBOM and no artifact", rel.SourceSBOM, rel.Artifacts)
	}
	store, err := openRecordSBOMStore(projectDir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSBOM(e.Digest); err != nil {
		t.Errorf("the machine ledger does not hold the source SBOM: %v", err)
	}

	// Without the flag again: still the same release, the SBOM kept.
	if again, err := recordReleaseIn(context.Background(), plain, sc, &bytes.Buffer{}); err != nil || again.ReleaseCreated {
		t.Fatalf("plain re-record: %+v %v", again, err)
	}
	if rel := recorded(); rel.SourceSBOM == nil || rel.SourceSBOM.Digest != e.Digest {
		t.Errorf("the source SBOM was dropped: %+v", rel.SourceSBOM)
	}

	var out bytes.Buffer
	printReleaseRecord(&out, res)
	if !strings.Contains(out.String(), "sbom     (source) "+e.Digest) {
		t.Errorf("text output does not list the source SBOM:\n%s", out.String())
	}
}

// A source tree syft cannot read warns, records the source SBOM as
// unavailable, and the record succeeds.
func TestReleaseRecordSBOM_SourceTreeFailureWarns(t *testing.T) {
	rt := newSBOMRecordTest(t)
	rt.syft.fail = map[string]error{"dir": errors.New("permission denied")}
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(),
		releaseRecordOptions{env: "prod", version: "v1.4.0", sbom: recordSBOMOptions{source: t.TempDir()}}, rt.sc, &warn)
	if err != nil {
		t.Fatalf("a failed source SBOM failed the record: %v", err)
	}
	if len(res.SBOMs) != 1 || res.SBOMs[0].Status != release.SBOMStatusUnavailable || !strings.Contains(res.SBOMs[0].Reason, "permission denied") {
		t.Errorf("sboms: %+v", res.SBOMs)
	}
	if !strings.Contains(warn.String(), "without an SBOM of its source") {
		t.Errorf("warning: %q", warn.String())
	}
	if arts, _ := rt.cp.called("CutRelease")[0]["artifacts"].([]any); len(arts) != 0 {
		t.Errorf("--sbom-source added artifacts: %v", arts)
	}
}

// The post-generate hooks run for record-made SBOMs too, generated and
// provided alike: Name is the workload, the repository is Artifact.URI.
func TestReleaseRecordSBOM_RunsThePostGenerateHooks(t *testing.T) {
	rt := newSBOMRecordTest(t)
	prev := sbomPostGenerate
	t.Cleanup(func() { sbomPostGenerate = prev })
	var seen sync.Map
	sbomPostGenerate = []sbomPostGenerateHook{func(_ context.Context, in sbomGenerated) (release.SBOMRef, error) {
		seen.Store(in.Name, in.Artifact.URI+" "+in.Version+" "+in.Env)
		ref := *in.Artifact.SBOM
		ref.Referrer = in.Artifact.URI + "@" + sha("referrer")
		return ref, nil
	}}
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	o := twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{specs: []string{"api=" + cdx}}})
	res, err := recordReleaseIn(context.Background(), o, rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	for _, w := range []string{"api", "web"} {
		if got, _ := seen.Load(w); got != "ghcr.io/acme/"+w+" v1.4.0 prod" {
			t.Errorf("%s: the hook saw %v", w, got)
		}
		if e := sbomEntry(t, res, w); e.Status != release.SBOMStatusOK {
			t.Errorf("%s: %+v", w, e)
		}
	}
}

// On the machine ledger the release itself carries each SBOM ref, and
// --json lists them under sboms.
func TestReleaseRecordSBOM_MachineLedgerAndJSON(t *testing.T) {
	newSBOMRecordTest(t)
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ledgerSelectionEnv, string(ledgerSelectMachine))
	useRecordLedger(t, defaultRecordLedger)
	sc := recordScope{projectDir: projectDir, project: "shop", getenv: func(string) string { return "" }}
	o := twoWorkloads(releaseRecordOptions{commit: recordCommitA})

	res, err := recordReleaseIn(context.Background(), o, sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	l, err := machineLedger(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := l.Releases.Get(context.Background(), "v1.4.0")
	if err != nil || rel == nil {
		t.Fatalf("release: %v", err)
	}
	if ref := rel.Artifacts["api"].SBOM; ref == nil || ref.Digest != sbomEntry(t, res, "api").Digest || ref.Source != release.SBOMSourceRegistry {
		t.Errorf("recorded api SBOM %+v", ref)
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SBOMs []map[string]any `json:"sboms"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.SBOMs) != 2 {
		t.Fatalf("--json sboms: %s", raw)
	}
	first := doc.SBOMs[0]
	for _, key := range []string{"workload", "digest", "status", "source"} {
		if _, ok := first[key]; !ok {
			t.Errorf("--json sboms[0] has no %q: %v", key, first)
		}
	}
	if _, ok := first["reason"]; ok {
		t.Errorf("an SBOM that was recorded carries a reason: %v", first)
	}
	var out bytes.Buffer
	printReleaseRecord(&out, res)
	if !strings.Contains(out.String(), "sbom     api") {
		t.Errorf("text output lists no SBOM:\n%s", out.String())
	}
}

// Registry reads go through the cut's credential path: the record's env and
// its declaration decide whether forge's own control-plane identity is
// offered for the image's registry.
func TestReleaseRecordSBOM_RegistryReadsUseTheCutsCredentialPath(t *testing.T) {
	rt := newSBOMRecordTest(t)
	prev := sbomRegistryCredential
	t.Cleanup(func() { sbomRegistryCredential = prev })
	declared := &KCLEntities{}
	var mu sync.Mutex
	var asked []string
	sbomRegistryCredential = func(projectDir, env string, e *KCLEntities, host string) (auth.CredentialFunc, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, env+" "+host+" "+strconv.FormatBool(e == declared))
		return nil, nil
	}
	artifacts := map[string]release.Artifact{"api": {Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: recordDigestA}, URI: "registry.example.com/acme/api"}}
	recordReleaseSBOMs(context.Background(), recordSBOMRun{projectDir: rt.sc.projectDir, project: "shop", env: "prod",
		version: "v1.4.0", declared: declared}, artifacts, &bytes.Buffer{})
	if len(asked) != 1 || asked[0] != "prod registry.example.com true" {
		t.Errorf("credential asked for %v, want prod's declaration for registry.example.com", asked)
	}
	if ref := artifacts["api"].SBOM; ref == nil || ref.Status != release.SBOMStatusOK {
		t.Errorf("SBOM %+v", ref)
	}
}

// A recorded image is described exactly as the cut describes it (the
// repository as the source name, no release version), so an image forge
// already cut is a cache hit for a record of another version, and the two
// name one SBOM.
func TestReleaseRecordSBOM_SharesTheCutsSBOMForAnImage(t *testing.T) {
	rt := newSBOMRecordTest(t)
	cache := t.TempDir()
	useFakeSyft(t, rt.syft, cache)
	dir := newLedgerTestProject(t, "sbomshare")
	cut := map[string]release.Artifact{"ghcr.io/acme/api": sbomTestImage(recordDigestA)}
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: "v1.3.0"}, cut, &bytes.Buffer{})
	if !cut["ghcr.io/acme/api"].SBOM.HasSBOM() {
		t.Fatalf("cut: %+v", cut["ghcr.io/acme/api"].SBOM)
	}
	before := len(rt.syft.calls())

	o := releaseRecordOptions{env: "prod", version: "v1.4.0", workloads: []string{"api"}, images: []string{"ghcr.io/acme/api@" + recordDigestA}}
	res, err := recordReleaseIn(context.Background(), o, rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if n := len(rt.syft.calls()) - before; n != 0 {
		t.Errorf("the record ran syft %d time(s) for an image the cut already described", n)
	}
	if got := sbomEntry(t, res, "api").Digest; got != cut["ghcr.io/acme/api"].SBOM.Digest {
		t.Errorf("record SBOM %s, cut SBOM %s: want one SBOM", got, cut["ghcr.io/acme/api"].SBOM.Digest)
	}
}

// useOffPolicyEnv declares the record's env prod in deploy/kcl, every
// workload external, with provenance_sbom off.
func useOffPolicyEnv(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, "deploy", "kcl", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "deploy", "kcl", "prod", "main.k"), []byte("# prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := release.DefaultDeliveryPolicy()
	policy.ProvenanceSBOM.Mode = release.DeliveryOff
	off := &KCLEntities{DeliveryPolicy: &policy, Workloads: []WorkloadEntity{externalWL("api"), externalWL("web")}}
	prev := renderRecordEnv
	renderRecordEnv = func(context.Context, string, string) (*KCLEntities, error) { return off, nil }
	t.Cleanup(func() { renderRecordEnv = prev })
}

// Explicit evidence is never discarded: under provenance_sbom off, an
// --sbom file is recorded (and listed in --json and the text output), while
// the workload named only by --image gets no SBOM and no entry, and syft
// never runs.
func TestReleaseRecordSBOM_PolicyOffRecordsOnlyExplicitSBOMs(t *testing.T) {
	rt := newSBOMRecordTest(t)
	useOffPolicyEnv(t, rt.sc.projectDir)
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(),
		twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{specs: []string{"api=" + cdx}}}), rt.sc, &warn)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(res.SBOMs) != 1 {
		t.Fatalf("sboms = %+v, want only the supplied api SBOM", res.SBOMs)
	}
	e := res.SBOMs[0]
	if e.Workload != "api" || e.Status != release.SBOMStatusOK || e.Source != release.SBOMSourceCIProvided || e.Digest != sbom.Digest([]byte(recordCycloneDX)) {
		t.Errorf("api entry: %+v", e)
	}
	if data, err := rt.store(t).GetSBOM(e.Digest); err != nil || string(data) != recordCycloneDX {
		t.Errorf("the supplied SBOM is not in the machine ledger: %q %v", data, err)
	}
	if n := len(rt.syft.calls()); n != 0 {
		t.Errorf("syft ran %d time(s) under provenance_sbom off", n)
	}
	if !strings.Contains(warn.String(), "provenance_sbom off") || strings.Contains(warn.String(), "not recorded") {
		t.Errorf("output: %q", warn.String())
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"sboms":[{"workload":"api"`) || strings.Contains(string(raw), `"workload":"web"`) {
		t.Errorf("--json: %s", raw)
	}
	var out bytes.Buffer
	printReleaseRecord(&out, res)
	if !strings.Contains(out.String(), "sbom     api") || strings.Contains(out.String(), "sbom     web") {
		t.Errorf("text output:\n%s", out.String())
	}
	arts := rt.cp.called("CutRelease")[0]["artifacts"].([]any)
	if len(arts) != 2 {
		t.Errorf("CutRelease artifacts = %v", arts)
	}
}

// Under provenance_sbom off with no explicit SBOM, nothing is generated,
// nothing is listed, and nothing is said, as at the cut.
func TestReleaseRecordSBOM_PolicyOffSkipsImplicitGeneration(t *testing.T) {
	rt := newSBOMRecordTest(t)
	useOffPolicyEnv(t, rt.sc.projectDir)
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(), twoWorkloads(releaseRecordOptions{}), rt.sc, &warn)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(res.SBOMs) != 0 || len(rt.syft.calls()) != 0 || warn.Len() != 0 {
		t.Errorf("sboms %+v, syft %v, output %q; want none of them", res.SBOMs, rt.syft.calls(), warn.String())
	}
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), "sboms") {
		t.Errorf("--json lists sboms: %s", raw)
	}
}

// --sbom-source is an explicit request, recorded as the release's source
// SBOM under provenance_sbom off.
func TestReleaseRecordSBOM_PolicyOffRecordsTheSourceSBOM(t *testing.T) {
	rt := newSBOMRecordTest(t)
	useOffPolicyEnv(t, rt.sc.projectDir)
	tree := t.TempDir()
	res, err := recordReleaseIn(context.Background(),
		releaseRecordOptions{env: "prod", version: "v1.4.0", sbom: recordSBOMOptions{source: tree}}, rt.sc, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(res.SBOMs) != 1 || res.SBOMs[0].Workload != "" || res.SBOMs[0].Status != release.SBOMStatusOK || res.SBOMs[0].Source != release.SBOMSourceLockfile {
		t.Fatalf("sboms = %+v, want the source SBOM", res.SBOMs)
	}
	if calls := rt.syft.calls(); len(calls) != 1 || calls[0] != "dir:"+tree {
		t.Errorf("syft read %v, want a dir scan of %s", calls, tree)
	}
	if _, err := rt.store(t).GetSBOM(res.SBOMs[0].Digest); err != nil {
		t.Errorf("the source SBOM is not in the machine ledger: %v", err)
	}
}

// --no-sbom with --sbom is contradictory whatever the policy, and refused
// before anything is cut.
func TestReleaseRecordSBOM_NoSBOMWithSBOMIsRefusedUnderPolicyOff(t *testing.T) {
	rt := newSBOMRecordTest(t)
	useOffPolicyEnv(t, rt.sc.projectDir)
	cdx := writeSBOMFile(t, "api.cdx.json", recordCycloneDX)
	_, err := recordReleaseIn(context.Background(),
		twoWorkloads(releaseRecordOptions{sbom: recordSBOMOptions{noSBOM: true, specs: []string{"api=" + cdx}}}), rt.sc, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err %v, want the contradiction refused", err)
	}
	if n := len(rt.cp.called("CutRelease")); n != 0 {
		t.Errorf("a refused record cut %d release(s)", n)
	}
}

// A recorded image gets its SBOM attached beside it as a referrer, through
// the cut's hook: the CI recording it pushed it (Built), the referrer is
// stamped with the release's CreatedAt, and the release records where it is.
// A supplied --sbom is attached the same way as a generated one.
func TestReleaseRecordSBOM_AttachesReferrersBesideRecordedImages(t *testing.T) {
	newSBOMRecordTest(t)
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	apiRepo, apiDigest := splitSubject(f.PutSubject("acme/api", "v1"))
	webRepo, webDigest := splitSubject(f.PutSubject("acme/web", "v1"))
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ledgerSelectionEnv, string(ledgerSelectMachine))
	useRecordLedger(t, defaultRecordLedger)
	sc := recordScope{projectDir: projectDir, project: "shop", getenv: func(string) string { return "" }}
	cdx := writeSBOMFile(t, "web.cdx.json", recordCycloneDX)
	o := releaseRecordOptions{env: "prod", version: "v1.4.0", commit: recordCommitA,
		workloads: []string{"api", "web"}, images: []string{apiRepo + "@" + apiDigest, webRepo + "@" + webDigest},
		sbom: recordSBOMOptions{specs: []string{"web=" + cdx}}}

	var warn bytes.Buffer
	if _, err := recordReleaseIn(context.Background(), o, sc, &warn); err != nil {
		t.Fatalf("record: %v", err)
	}
	if strings.Contains(warn.String(), "not attached") {
		t.Errorf("an attach failed:\n%s", warn.String())
	}
	l, err := machineLedger(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := l.Releases.Get(context.Background(), "v1.4.0")
	if err != nil || rel == nil {
		t.Fatalf("release: %v", err)
	}
	subjects := map[string]string{}
	for _, c := range *calls {
		if !c.opts.Created.Equal(rel.CreatedAt) || c.opts.Release != "v1.4.0" {
			t.Errorf("attach %s: created %v release %q, want the release's CreatedAt %v and v1.4.0", c.subject, c.opts.Created, c.opts.Release, rel.CreatedAt)
		}
		subjects[c.subject] = c.opts.Generator
	}
	for w, subject := range map[string]string{"api": apiRepo + "@" + apiDigest, "web": webRepo + "@" + webDigest} {
		if _, ok := subjects[subject]; !ok {
			t.Errorf("%s: no attach attempt for %s (got %v)", w, subject, subjects)
		}
		ref := rel.Artifacts[w].SBOM
		repo, _ := splitSubject(subject)
		if ref == nil || !strings.HasPrefix(ref.Referrer, repo+"@sha256:") {
			t.Errorf("%s: recorded SBOM %+v, want a referrer beside %s", w, ref, repo)
		}
	}
	if got := subjects[webRepo+"@"+webDigest]; got != "trivy@0.50.0" {
		t.Errorf("the supplied SBOM was attached with generator %q, want the document's", got)
	}
}

// A record refused as a conflict, before anything is cut, runs no syft and
// attaches nothing beside the images.
func TestReleaseRecordSBOM_AConflictingRecordRunsNoSyftAndAttachesNothing(t *testing.T) {
	rt := newSBOMRecordTest(t)
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	repo, digestA := splitSubject(f.PutSubject("acme/api", "v1"))
	_, digestB := splitSubject(f.PutSubject("acme/api", "v2"))
	rec := func(digest string) error {
		_, err := recordReleaseIn(context.Background(), releaseRecordOptions{env: "prod", version: "v1.4.0",
			workloads: []string{"api"}, images: []string{repo + "@" + digest}}, rt.sc, &bytes.Buffer{})
		return err
	}
	if err := rec(digestA); err != nil {
		t.Fatalf("first record: %v", err)
	}
	syftBefore, attachBefore := len(rt.syft.calls()), len(*calls)
	if attachBefore != 1 {
		t.Fatalf("the accepted record attached %d time(s), want 1", attachBefore)
	}

	err := rec(digestB)
	if !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("err = %v, want the conflict", err)
	}
	if n := len(rt.syft.calls()) - syftBefore; n != 0 {
		t.Errorf("a refused record ran syft %d time(s)", n)
	}
	if n := len(*calls) - attachBefore; n != 0 {
		t.Errorf("a refused record attached %d referrer(s)", n)
	}
	if n := len(rt.cp.called("CutRelease")); n != 1 {
		t.Errorf("CutRelease called %d times, want only the first record's", n)
	}
}

// The hooks run after the promotion. A promotion the control plane refuses
// after the cut leaves a recorded release with nothing attached beside its
// image (generation already ran; it writes only to this machine), and the
// retry, an idempotent cut and a promotion, attaches then. On a hosted
// ledger the referrer is listed in the result, and the output says the
// control plane does not record it yet.
func TestReleaseRecordSBOM_APromotionFailureAttachesNothingUntilTheRetry(t *testing.T) {
	rt := newSBOMRecordTest(t)
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	repo, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	o := releaseRecordOptions{env: "prod", version: "v1.4.0", workloads: []string{"api"}, images: []string{repo + "@" + digest}}

	rt.cp.promoteErr = &cloud.Error{Code: "failed_precondition", Message: "environment is pinned"}
	if _, err := recordReleaseIn(context.Background(), o, rt.sc, &bytes.Buffer{}); err == nil {
		t.Fatal("the refused promotion was not reported")
	}
	if len(rt.cp.called("CutRelease")) != 1 {
		t.Fatal("the release was not cut before the promotion")
	}
	if n := len(*calls); n != 0 {
		t.Fatalf("a record whose promotion failed attached %d referrer(s)", n)
	}

	rt.cp.promoteErr = nil
	var warn bytes.Buffer
	res, err := recordReleaseIn(context.Background(), o, rt.sc, &warn)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.ReleaseCreated || !res.Promoted || len(*calls) != 1 {
		t.Fatalf("retry: %+v, %d attach(es); want the same release, promoted, attached once", res, len(*calls))
	}
	if e := sbomEntry(t, res, "api"); !strings.HasPrefix(e.Referrer, repo+"@sha256:") {
		t.Errorf("api entry %+v, want the referrer", e)
	}
	if !strings.Contains(warn.String(), "does not record SBOM references yet") {
		t.Errorf("the hosted note is missing:\n%s", warn.String())
	}
}

// On the machine ledger a retry neither re-attaches nor loses the referrer:
// the first record backfilled it, and the hooks act on what is recorded.
func TestReleaseRecordSBOM_RetryKeepsTheRecordedReferrer(t *testing.T) {
	newSBOMRecordTest(t)
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	repo, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ledgerSelectionEnv, string(ledgerSelectMachine))
	useRecordLedger(t, defaultRecordLedger)
	sc := recordScope{projectDir: projectDir, project: "shop", getenv: func(string) string { return "" }}
	o := releaseRecordOptions{env: "prod", version: "v1.4.0", commit: recordCommitA, workloads: []string{"api"}, images: []string{repo + "@" + digest}}

	first, err := recordReleaseIn(context.Background(), o, sc, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := recordReleaseIn(context.Background(), o, sc, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n != 1 {
		t.Errorf("attached %d time(s) over a record and its retry, want 1", n)
	}
	want := sbomEntry(t, first, "api").Referrer
	if want == "" || sbomEntry(t, again, "api").Referrer != want {
		t.Errorf("referrer %q then %q", want, sbomEntry(t, again, "api").Referrer)
	}
}
