package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testsCommit = "0123456789abcdef0123456789abcdef01234567"

func testsRelease(ref *TestsRef) Release {
	r := sbomTestRelease(nil)
	r.Git = Git{Commit: testsCommit}
	r.Tests = ref
	return r
}

func okTests(c string, lanes ...string) *TestsRef {
	return &TestsRef{
		Commit: testsCommit, ReportDigest: sbomTestDigest(c), Lanes: lanes,
		Totals:   &TestsTotals{Passed: 10, Failed: 0, Skipped: 1},
		Coverage: &TestsCoverage{Combined: 61.5, ByLane: map[string]float64{"unit": 61.5}},
		Status:   TestsStatusOK,
	}
}

func partialTests(c string, lanes ...string) *TestsRef {
	t := okTests(c, lanes...)
	t.Status, t.Reason = TestsStatusPartial, "no integration lane"
	return t
}

// A re-cut of the same digests is the same release whatever its test
// evidence says.
func TestSameContentIgnoresTests(t *testing.T) {
	t.Parallel()
	none := testsRelease(nil)
	some := testsRelease(okTests("c", "unit"))
	other := testsRelease(&TestsRef{Status: TestsStatusUnavailable, Reason: "no runs"})
	for _, pair := range [][2]Release{{none, some}, {some, none}, {some, other}} {
		if !pair[0].SameContent(pair[1]) {
			t.Fatalf("test evidence changed the release's identity")
		}
		if err := CheckRecut(pair[0], pair[1]); err != nil {
			t.Fatalf("CheckRecut refused a tests-only difference: %v", err)
		}
	}
}

func TestBackfillTestsFillsButNeverReplaces(t *testing.T) {
	t.Parallel()
	unavailable := &TestsRef{Status: TestsStatusUnavailable, Reason: "no runs recorded"}
	tests := []struct {
		name        string
		existing    *TestsRef
		requested   *TestsRef
		wantChanged bool
		want        *TestsRef
	}{
		{name: "record has none: filled", requested: okTests("c", "unit"), wantChanged: true, want: okTests("c", "unit")},
		{name: "record has none, retry says unavailable: recorded", requested: unavailable, wantChanged: true, want: unavailable},
		{name: "record ok: kept, never replaced", existing: okTests("c", "unit"), requested: okTests("d", "integration", "unit"), want: okTests("c", "unit")},
		{name: "record unavailable: a report fills it", existing: unavailable, requested: partialTests("d", "unit"), wantChanged: true, want: partialTests("d", "unit")},
		{name: "both unavailable: the first reason stands", existing: unavailable, requested: &TestsRef{Status: TestsStatusUnavailable, Reason: "other"}, want: unavailable},
		{name: "record partial: more lanes supersede it", existing: partialTests("c", "unit"), requested: okTests("d", "integration", "unit"), wantChanged: true, want: okTests("d", "integration", "unit")},
		{name: "record partial: the same lanes do not", existing: partialTests("c", "unit"), requested: okTests("d", "unit"), want: partialTests("c", "unit")},
		{name: "record partial: a lane lost does not", existing: partialTests("c", "e2e", "unit"), requested: okTests("d", "integration", "unit"), want: partialTests("c", "e2e", "unit")},
		{name: "record partial: unavailable never replaces it", existing: partialTests("c", "unit"), requested: unavailable, want: partialTests("c", "unit")},
		{name: "retry carries none: nothing to do", existing: okTests("c", "unit"), want: okTests("c", "unit")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			existing := testsRelease(tt.existing)
			before, _ := json.Marshal(existing)
			got, changed := BackfillTests(existing, testsRelease(tt.requested))
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if after, _ := json.Marshal(existing); !bytes.Equal(before, after) {
				t.Fatalf("BackfillTests modified the existing record")
			}
			if !reflect.DeepEqual(got.Tests, tt.want) {
				t.Fatalf("Tests = %+v, want %+v", got.Tests, tt.want)
			}
			if !got.SameContent(existing) {
				t.Fatalf("backfill changed the release's identity")
			}
		})
	}
}

func TestReleaseValidateTests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  *TestsRef
		ok   bool
	}{
		{"none", nil, true},
		{"ok", okTests("c", "unit"), true},
		{"partial", partialTests("c", "unit"), true},
		{"unavailable with a reason", &TestsRef{Status: TestsStatusUnavailable, Reason: "dirty tree"}, true},
		// An open vocabulary on read.
		{"unknown status", &TestsRef{Commit: testsCommit, ReportDigest: sbomTestDigest("c"), Status: "stale"}, true},
		{"not a digest", &TestsRef{ReportDigest: "latest", Status: TestsStatusOK}, false},
		{"ok without a digest", &TestsRef{Status: TestsStatusOK}, false},
		{"partial without a digest", &TestsRef{Status: TestsStatusPartial}, false},
		{"another commit's report", &TestsRef{Commit: strings.Repeat("f", 40), ReportDigest: sbomTestDigest("c"), Status: TestsStatusOK}, false},
		{"negative totals", &TestsRef{Totals: &TestsTotals{Failed: -1}, Status: TestsStatusUnavailable}, false},
		{"coverage over 100", &TestsRef{ReportDigest: sbomTestDigest("c"), Status: TestsStatusOK, Coverage: &TestsCoverage{Combined: 101}}, false},
		{"lane coverage below 0", &TestsRef{ReportDigest: sbomTestDigest("c"), Status: TestsStatusOK, Coverage: &TestsCoverage{Combined: 1, ByLane: map[string]float64{"unit": -1}}}, false},
	}
	for _, tt := range tests {
		err := testsRelease(tt.ref).Validate()
		if (err == nil) != tt.ok {
			t.Fatalf("%s: Validate = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v does not wrap ErrInvalid", tt.name, err)
		}
	}
}

// preTestsRelease is the release record as a forge without Release.Tests
// declares it, trimmed to the fields this line carries.
type preTestsRelease struct {
	Version    string              `json:"release"`
	Git        Git                 `json:"git"`
	Migrations *MigrationManifest  `json:"migrations,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
	Artifacts  map[string]Artifact `json:"artifacts"`
	Run        Run                 `json:"run,omitempty"`
}

// TestOlderDecoderIgnoresTests is the semver proof for Release.Tests: a
// release line this forge writes, read by a forge that predates the field
// through the decoder every lenient reader uses (the machine ledger's
// decodeAll and the hosted-ledger mirror are plain json.Unmarshal), decodes
// to the same release with the field dropped.
//
// The STRICT readers of a Release — a snapshot release's CutRelease document,
// decoded with DisallowUnknownFields by the control plane and by forge's
// snapshotReleaseFromJSON — would refuse it, which is why the hosted ledger
// sends a snapshot release's document without its test evidence, as it does
// its SBOMs and migration manifest.
func TestOlderDecoderIgnoresTests(t *testing.T) {
	t.Parallel()
	r := testsRelease(okTests("c", "integration", "unit"))
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte(`"tests":{`)) {
		t.Fatalf("the line under test carries no tests: %s", line)
	}

	var old preTestsRelease
	if err := json.Unmarshal(line, &old); err != nil {
		t.Fatalf("an older forge's decoder refused a line with tests: %v", err)
	}
	if old.Version != r.Version || old.Git != r.Git || len(old.Artifacts) != len(r.Artifacts) {
		t.Fatalf("decoded %+v", old)
	}
	for name, a := range r.Artifacts {
		if !old.Artifacts[name].sameIdentity(a) {
			t.Fatalf("%s: decoded %+v, want the identity of %+v", name, old.Artifacts[name], a)
		}
	}

	// A release without test evidence serializes exactly as before.
	plain, _ := json.Marshal(testsRelease(nil))
	if bytes.Contains(plain, []byte(`"tests"`)) {
		t.Fatalf("a release without test evidence names the field: %s", plain)
	}

	// And the counterfactual that makes the hosted strip necessary: the old
	// shape reads the line without tests strictly, and refuses it with them.
	strict := json.NewDecoder(bytes.NewReader(plain))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&preTestsRelease{}); err != nil {
		t.Fatalf("the old shape is not the shape of a line without tests: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&preTestsRelease{}); err == nil {
		t.Fatalf("a strict decoder accepted an unknown field; the snapshot strip's premise changed")
	}
}
