package ledgerfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const testSBOM = `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`

func TestSBOMRoundTripIsContentAddressed(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest, err := s.PutSBOM([]byte(testSBOM))
	if err != nil {
		t.Fatal(err)
	}
	if digest != sbomDigest([]byte(testSBOM)) || !release.ValidDigest(digest) {
		t.Fatalf("PutSBOM digest = %q", digest)
	}
	path := filepath.Join(s.Dir(), "sboms", "sha256-"+strings.TrimPrefix(digest, "sha256:")+".cdx.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("SBOM not at its content address: %v", err)
	}
	// Windows has no POSIX permission bits (os.Stat reports 0666 for any
	// writable file), so the mode is only meaningful elsewhere.
	if runtime.GOOS != "windows" && info.Mode().Perm() != fileMode {
		t.Fatalf("SBOM mode = %v, want %v", info.Mode().Perm(), os.FileMode(fileMode))
	}
	got, err := s.GetSBOM(digest)
	if err != nil || string(got) != testSBOM {
		t.Fatalf("GetSBOM = %q, %v", got, err)
	}
	again, err := s.PutSBOM([]byte(testSBOM))
	if err != nil || again != digest {
		t.Fatalf("second PutSBOM = %q, %v", again, err)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestSBOMDigestIsVerifiedOnRead(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest, err := s.PutSBOM([]byte(testSBOM))
	if err != nil {
		t.Fatal(err)
	}
	path := s.path(sbomsDir, "sha256-"+strings.TrimPrefix(digest, "sha256:")+sbomSuffix)
	if err := os.WriteFile(path, []byte(`{"tampered":true}`), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSBOM(digest); !errors.Is(err, ErrSBOMCorrupt) {
		t.Fatalf("GetSBOM served bytes that do not match their digest: %v", err)
	}
	// Writing the right bytes again repairs it.
	if _, err := s.PutSBOM([]byte(testSBOM)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSBOM(digest); err != nil || string(got) != testSBOM {
		t.Fatalf("after repair GetSBOM = %q, %v", got, err)
	}
}

func TestSBOMRefusals(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.GetSBOM("sha256:" + strings.Repeat("0", 64)); !errors.Is(err, ErrSBOMNotFound) {
		t.Fatalf("missing SBOM: %v", err)
	}
	for _, d := range []string{"", "latest", "sha256:../../x", "sha256:" + strings.Repeat("A", 64)} {
		if _, err := s.GetSBOM(d); !errors.Is(err, release.ErrInvalid) {
			t.Fatalf("GetSBOM(%q) = %v, want ErrInvalid", d, err)
		}
	}
	for _, data := range []string{"", "not json"} {
		if _, err := s.PutSBOM([]byte(data)); !errors.Is(err, release.ErrInvalid) {
			t.Fatalf("PutSBOM(%q) = %v, want ErrInvalid", data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), sbomsDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused SBOM left the store directory behind: %v", err)
	}
}

func findings(digest string, built time.Time, body string) FindingsRecord {
	return FindingsRecord{
		SBOMDigest:     digest,
		Scanner:        "grype",
		ScannerVersion: "v0.122.1",
		DBSchema:       "v6",
		DBBuiltAt:      built,
		ScannedAt:      built.Add(time.Hour),
		Body:           json.RawMessage(body),
	}
}

func TestFindingsAreFiledPerDBBuild(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest, err := s.PutSBOM([]byte(testSBOM))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestFindings(digest); got != nil || err != nil {
		t.Fatalf("never scanned: LatestFindings = %+v, %v", got, err)
	}
	day1 := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	// Written newest first: the order on read is the DB builds', not the writes'.
	for _, r := range []FindingsRecord{findings(digest, day2, `{"n":2}`), findings(digest, day1, `{"n":1}`)} {
		if err := s.PutFindings(r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListFindings(digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || !all[0].DBBuiltAt.Equal(day1) || !all[1].DBBuiltAt.Equal(day2) {
		t.Fatalf("ListFindings = %+v", all)
	}
	latest, err := s.LatestFindings(digest)
	if err != nil || latest == nil || string(latest.Body) != `{"n":2}` {
		t.Fatalf("LatestFindings = %+v, %v", latest, err)
	}

	// A rescan against the same DB build replaces that build's result.
	if err := s.PutFindings(findings(digest, day2, `{"n":3}`)); err != nil {
		t.Fatal(err)
	}
	all, err = s.ListFindings(digest)
	if err != nil || len(all) != 2 || string(all[1].Body) != `{"n":3}` {
		t.Fatalf("after rescan ListFindings = %+v, %v", all, err)
	}
	stem, _ := sbomFileStem(digest)
	if _, err := os.Stat(filepath.Join(s.Dir(), "findings", stem, "20261120T030000Z_grype@v0.122.1.json")); err != nil {
		t.Fatalf("findings not filed under the DB build and scanner: %v", err)
	}
	assertNoTempFiles(t, filepath.Join(s.Dir(), "findings", stem))
}

// B1 (forge#2480 review): a scan by another scanner, or another version of
// one, against the same DB build is more evidence, never a replacement.
func TestFindingsKeepEveryScannerOfOneDBBuild(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest, err := s.PutSBOM([]byte(testSBOM))
	if err != nil {
		t.Fatal(err)
	}
	built := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	grype := findings(digest, built, `{"by":"grype v0.122.1"}`)
	newerGrype := findings(digest, built, `{"by":"grype v0.123.0"}`)
	newerGrype.ScannerVersion, newerGrype.ScannedAt = "v0.123.0", built.Add(2*time.Hour)
	other := findings(digest, built, `{"by":"osv"}`)
	other.Scanner, other.ScannerVersion, other.ScannedAt = "osv-scanner", "v2.0.0", built.Add(3*time.Hour)
	// Two scanner names that flatten to the same file-name text.
	slashed := findings(digest, built, `{"by":"a/b"}`)
	slashed.Scanner, slashed.ScannerVersion, slashed.ScannedAt = "a/b", "", built.Add(4*time.Hour)
	underscored := findings(digest, built, `{"by":"a_b"}`)
	underscored.Scanner, underscored.ScannerVersion, underscored.ScannedAt = "a_b", "", built.Add(5*time.Hour)

	// Written out of scan order: the read order is the records' own.
	for _, r := range []FindingsRecord{underscored, other, grype, slashed, newerGrype} {
		if err := s.PutFindings(r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListFindings(digest)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range all {
		got = append(got, string(r.Body))
	}
	want := []string{`{"by":"grype v0.122.1"}`, `{"by":"grype v0.123.0"}`, `{"by":"osv"}`, `{"by":"a/b"}`, `{"by":"a_b"}`}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ListFindings = %v, want every scanner's record in scan order %v", got, want)
	}
	latest, err := s.LatestFindings(digest)
	if err != nil || latest == nil || string(latest.Body) != `{"by":"a_b"}` {
		t.Fatalf("LatestFindings = %+v, %v; want the scan that ran last", latest, err)
	}

	// The same scanner, version and DB build replaces only its own file.
	again := grype
	again.Body, again.ScannedAt = json.RawMessage(`{"by":"grype v0.122.1, again"}`), built.Add(6*time.Hour)
	if err := s.PutFindings(again); err != nil {
		t.Fatal(err)
	}
	all, err = s.ListFindings(digest)
	if err != nil || len(all) != len(want) {
		t.Fatalf("after a re-evaluation ListFindings has %d records, %v; want %d", len(all), err, len(want))
	}
	if last := all[len(all)-1]; string(last.Body) != `{"by":"grype v0.122.1, again"}` {
		t.Fatalf("re-evaluation did not replace its own record: last = %s", last.Body)
	}
}

// A newer DB build outranks a later scan of an older one.
func TestFindingsOrderByDBBuildBeforeScanTime(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest := "sha256:" + strings.Repeat("e", 64)
	day1 := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	newDB := findings(digest, day1.Add(24*time.Hour), `{"db":"new"}`)
	newDB.ScannedAt = day1.Add(25 * time.Hour)
	lateOldDB := findings(digest, day1, `{"db":"old"}`)
	lateOldDB.Scanner, lateOldDB.ScannedAt = "osv-scanner", day1.Add(48*time.Hour)
	for _, r := range []FindingsRecord{newDB, lateOldDB} {
		if err := s.PutFindings(r); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestFindings(digest)
	if err != nil || latest == nil || string(latest.Body) != `{"db":"new"}` {
		t.Fatalf("LatestFindings = %+v, %v; want the newest DB build", latest, err)
	}
}

func TestFindingsRefusals(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest := "sha256:" + strings.Repeat("c", 64)
	built := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	bad := []FindingsRecord{
		findings("latest", built, `{}`),
		findings(digest, time.Time{}, `{}`),
		findings(digest, built, `not json`),
		findings(digest, built, ``),
		{SBOMDigest: digest, DBBuiltAt: built, Body: json.RawMessage(`{}`)},
	}
	for i, r := range bad {
		if err := s.PutFindings(r); !errors.Is(err, release.ErrInvalid) {
			t.Fatalf("record %d: PutFindings = %v, want ErrInvalid", i, err)
		}
	}

	// A file filed under another SBOM is an error, never that SBOM's findings.
	other := "sha256:" + strings.Repeat("d", 64)
	if err := s.PutFindings(findings(other, built, `{}`)); err != nil {
		t.Fatal(err)
	}
	src, _ := s.findingsPath(findings(other, built, `{}`))
	dst, _ := s.findingsPath(findings(digest, built, `{}`))
	if err := os.MkdirAll(filepath.Dir(dst), dirMode); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(src) //nolint:gosec // test temp dir
	if err := os.WriteFile(dst, data, fileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListFindings(digest); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("misfiled findings read as this SBOM's: %v", err)
	}
}

func TestWriteFileAtomicReplacesWhole(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "f.json")
	for _, content := range []string{"first, and longer than the second\n", "second\n"} {
		if err := writeFileAtomic(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path) //nolint:gosec // test temp dir
		if err != nil || string(got) != content {
			t.Fatalf("read %q, %v; want %q", got, err, content)
		}
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func sbomRelease(version string, ref *release.SBOMRef) release.Release {
	r := ociRelease(version)
	a := r.Artifacts["ghcr.io/acme/api"]
	a.SBOM = ref
	r.Artifacts["ghcr.io/acme/api"] = a
	return r
}

func sbomRef(c, generator string) *release.SBOMRef {
	return &release.SBOMRef{Digest: "sha256:" + strings.Repeat(c, 64), Generator: generator, Status: release.SBOMStatusOK}
}

func releaseLines(t *testing.T, s *Store) int {
	t.Helper()
	lines, err := readLines(s.path(releasesFile))
	if err != nil {
		t.Fatal(err)
	}
	return len(lines)
}

// A re-cut is a retry whatever its SBOMs say. One that knows an SBOM the
// record lacks backfills it; one that knows a different SBOM changes nothing.
func TestCutReleaseBackfillsSBOMButNeverReplaces(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	const api = "ghcr.io/acme/api"
	if created, err := s.CutRelease(sbomRelease("v1.0.0", nil)); err != nil || !created {
		t.Fatalf("first cut: %v, %v", created, err)
	}

	created, err := s.CutRelease(sbomRelease("v1.0.0", sbomRef("c", "syft@v1")))
	if err != nil || created {
		t.Fatalf("re-cut with an SBOM: created=%v err=%v (want a retry)", created, err)
	}
	got, err := s.Release("v1.0.0")
	if err != nil || got.Artifacts[api].SBOM == nil || got.Artifacts[api].SBOM.Digest != sbomRef("c", "").Digest {
		t.Fatalf("SBOM not backfilled: %+v, %v", got, err)
	}
	if n := releaseLines(t, s); n != 2 {
		t.Fatalf("backfill appended %d lines, want 2 in total", n)
	}

	if _, err := s.CutRelease(sbomRelease("v1.0.0", sbomRef("d", "syft@v2"))); err != nil {
		t.Fatalf("re-cut with a newer SBOM is a retry, not a conflict: %v", err)
	}
	if _, err := s.CutRelease(sbomRelease("v1.0.0", nil)); err != nil {
		t.Fatalf("re-cut with no SBOM: %v", err)
	}
	got, _ = s.Release("v1.0.0")
	if got.Artifacts[api].SBOM.Generator != "syft@v1" {
		t.Fatalf("the recorded SBOM was replaced: %+v", got.Artifacts[api].SBOM)
	}
	if n := releaseLines(t, s); n != 2 {
		t.Fatalf("a no-op re-cut appended: %d lines", n)
	}
}

func TestRecordReleaseSBOM(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	const api = "ghcr.io/acme/api"
	if _, err := s.CutRelease(sbomRelease("v1.0.0", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordReleaseSBOM("v9", api, *sbomRef("c", "syft@v1")); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := s.RecordReleaseSBOM("v1.0.0", "nope", *sbomRef("c", "syft@v1")); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("unknown artifact: %v", err)
	}
	if _, err := s.RecordReleaseSBOM("v1.0.0", api, release.SBOMRef{Digest: "latest"}); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("invalid ref: %v", err)
	}
	if recorded, err := s.RecordReleaseSBOM("v1.0.0", api, *sbomRef("c", "syft@v1")); err != nil || !recorded {
		t.Fatalf("backfill: %v, %v", recorded, err)
	}
	if recorded, err := s.RecordReleaseSBOM("v1.0.0", api, *sbomRef("d", "syft@v2")); err != nil || recorded {
		t.Fatalf("replace: recorded=%v err=%v (want kept)", recorded, err)
	}
	got, _ := s.Release("v1.0.0")
	if got.Artifacts[api].SBOM.Generator != "syft@v1" {
		t.Fatalf("SBOM = %+v", got.Artifacts[api].SBOM)
	}
}
