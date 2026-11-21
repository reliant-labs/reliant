package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/testreport"
)

const trTree = "0123456789abcdef0123456789abcdef01234567"

// fakeTestsService is control-plane's TestsService and the test half of
// DeployService/GetCapabilities, speaking Connect's JSON binding with the
// proto's field names spelled literally (a typo in the client's wire shapes
// fails the test rather than moving with it).
type fakeTestsService struct {
	mu sync.Mutex
	// features is GetCapabilities.testFeatures; noCapabilities answers
	// GetCapabilities Unimplemented.
	features       []string
	noCapabilities bool
	// record is how RecordTestRun answers: "ok", "replay", "unimplemented"
	// or "refuse" (InvalidArgument).
	record string
	// records are the RecordTestRun bodies, in order; calls every path.
	records []map[string]any
	calls   []string
}

func (f *fakeTestsService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	connectErr := func(status int, code, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"code":"`+code+`","message":"`+msg+`"}`)
	}
	switch r.URL.Path {
	case "/controlplane.v1.DeployService/GetCapabilities":
		if f.noCapabilities {
			connectErr(http.StatusNotImplemented, cloud.CodeUnimplemented, "no capabilities")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"testFeatures": f.features})
	case "/controlplane.v1.TestsService/RecordTestRun":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			connectErr(http.StatusBadRequest, cloud.CodeInvalidArgument, err.Error())
			return
		}
		f.records = append(f.records, body)
		switch f.record {
		case "unimplemented":
			connectErr(http.StatusNotImplemented, cloud.CodeUnimplemented, "recording test runs is not configured on this deployment")
		case "refuse":
			connectErr(http.StatusBadRequest, cloud.CodeInvalidArgument, "git_commit must be a full lowercase commit sha")
		case "replay":
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": "tr-1", "ingestState": "TEST_RUN_INGEST_STATE_INGESTED"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": "tr-1", "created": true, "ingestState": "TEST_RUN_INGEST_STATE_PENDING"})
		}
	default:
		connectErr(http.StatusNotFound, cloud.CodeUnimplemented, "no "+r.URL.Path)
	}
}

func (f *fakeTestsService) recorded() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.records...)
}

// trUploadProject is trProject with a run that can be uploaded (a full tree
// hash), pointed at fake: signed in with a token, project "shop".
func trUploadProject(t *testing.T, stream string, laneExit int, fake *fakeTestsService, edit func(*testreport.RunManifest)) string {
	t.Helper()
	root := trProject(t, stream, laneExit)
	path := filepath.Join(testreport.RunsRoot(root), "01RUN", testreport.RunManifestFile)
	m, err := testreport.ReadRunManifest(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	m.TreeHash = trTree
	if edit != nil {
		edit(&m)
	}
	trJSON(t, path, m)

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	isolatedCredentials(t)
	t.Setenv(cloud.DefaultTokenEnv, "rlat_test")
	t.Setenv(cloud.ConsoleURLEnv, "https://console.test")
	withProjectName(t, "shop")
	testUploadControlPlane = func(context.Context) (*cloud.Declaration, error) {
		return &cloud.Declaration{Endpoint: srv.URL}, nil
	}
	return root
}

func trReport(t *testing.T, args ...string) (testreport.Report, int) {
	t.Helper()
	out, code := trRun(t, newTestReportCmd(), append([]string{"--json"}, args...)...)
	var rep testreport.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	return rep, code
}

// gunzipPart decodes one TestUploadPart of a recorded body, checking it was
// sent gzip-encoded.
func gunzipPart(t *testing.T, body map[string]any, name string) (string, []byte) {
	t.Helper()
	part, ok := body[name].(map[string]any)
	if !ok {
		t.Fatalf("no %s part in %v", name, body)
	}
	if part["encoding"] != "TEST_PAYLOAD_ENCODING_GZIP" {
		t.Fatalf("%s encoding %v, want gzip", name, part["encoding"])
	}
	raw, err := base64.StdEncoding.DecodeString(part["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return part["format"].(string), doc
}

func TestTestReportUploadRecordsTheLaneRun(t *testing.T) {
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
	root := trUploadProject(t, trStream("pass"), 0, fake, nil)
	rep, code := trReport(t, "--upload")
	if code != exitOK {
		t.Fatalf("exit %d: %+v", code, rep)
	}
	if rep.Upload == nil || rep.Upload.Status != testUploadOK || rep.Upload.URL != "https://console.test/forge/p/shop/tests/runs/tr-1" {
		t.Fatalf("upload: %+v", rep.Upload)
	}
	recs := fake.recorded()
	if len(recs) != 1 {
		t.Fatalf("%d RecordTestRun calls", len(recs))
	}
	b := recs[0]
	for k, want := range map[string]any{
		"project": "shop", "gitCommit": trCommit, "treeHash": trTree, "branch": "feat",
		"lane": "unit", "source": "TEST_RUN_SOURCE_LOCAL", "exitCode": float64(0),
	} {
		if b[k] != want {
			t.Errorf("%s = %v, want %v", k, b[k], want)
		}
	}
	if _, ok := b["dirty"]; ok {
		t.Errorf("a clean run sent dirty: %v", b["dirty"])
	}
	format, results := gunzipPart(t, b, "results")
	stored, err := os.ReadFile(filepath.Join(testreport.RunsRoot(root), "01RUN", "unit", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if format != "go-json" || !bytes.Equal(results, stored) {
		t.Fatalf("results part %s is not the stored results file:\n%s", format, results)
	}
	format, cov := gunzipPart(t, b, "coverage")
	var doc testreport.Coverage
	if err := json.Unmarshal(cov, &doc); err != nil || format != testFormatCoverage {
		t.Fatalf("coverage part %s: %v\n%s", format, err, cov)
	}
	if len(doc.Files) != 1 || doc.Files[0].Path != "a/a.go" || doc.Files[0].BlobID == "" {
		t.Fatalf("coverage files: %+v", doc.Files)
	}
	// Never source: the documents hold paths, blob ids and counts only.
	src, err := os.ReadFile(filepath.Join(root, "a", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(b)
	for _, part := range [][]byte{cov, results, all} {
		for _, line := range []string{"return 1", "func F() int"} {
			if bytes.Contains(part, []byte(line)) {
				t.Fatalf("an uploaded document carries source %q (from %q)", line, src)
			}
		}
	}
}

func TestTestReportUploadOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		fake   *fakeTestsService // its answers; each subtest gets a fresh one
		stream string
		exit   int
		status string
		// plain and required are the exit codes of --upload and
		// --upload=required.
		plain, required int
	}{
		{"capability withheld", &fakeTestsService{record: "ok"}, trStream("pass"), 0, testUploadSkipped, exitOK, exitRefused},
		{"no GetCapabilities", &fakeTestsService{noCapabilities: true}, trStream("pass"), 0, testUploadSkipped, exitOK, exitRefused},
		{"RecordTestRun unimplemented", &fakeTestsService{features: []string{hostedTestFeatureResults}, record: "unimplemented"}, trStream("pass"), 0, testUploadSkipped, exitOK, exitRefused},
		{"refused (4xx)", &fakeTestsService{features: []string{hostedTestFeatureResults}, record: "refuse"}, trStream("pass"), 0, testUploadFailed, exitOK, exitRefused},
		{"idempotent replay", &fakeTestsService{features: []string{hostedTestFeatureResults}, record: "replay"}, trStream("pass"), 0, testUploadOK, exitOK, exitOK},
		{"a failed test outranks a required upload", &fakeTestsService{record: "ok"}, trStream("fail"), 1, testUploadSkipped, exitWrong, exitWrong},
	}
	for _, tc := range cases {
		for _, flag := range []string{"--upload", "--upload=required"} {
			t.Run(tc.name+" "+flag, func(t *testing.T) {
				fake := &fakeTestsService{features: tc.fake.features, noCapabilities: tc.fake.noCapabilities, record: tc.fake.record}
				trUploadProject(t, tc.stream, tc.exit, fake, nil)
				rep, code := trReport(t, flag)
				want := tc.plain
				if flag == "--upload=required" {
					want = tc.required
				}
				if code != want || rep.ExitCode != want {
					t.Fatalf("exit %d (document %d), want %d: %+v", code, rep.ExitCode, want, rep.Upload)
				}
				if rep.Upload == nil || rep.Upload.Status != tc.status {
					t.Fatalf("upload: %+v, want status %s", rep.Upload, tc.status)
				}
				if tc.status != testUploadOK && rep.Upload.Error == "" {
					t.Fatal("a skipped or failed upload says nothing about why")
				}
			})
		}
	}
}

func TestTestReportUploadSkipsADirtyTree(t *testing.T) {
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
	trUploadProject(t, trStream("pass"), 0, fake, func(m *testreport.RunManifest) { m.Dirty = true })
	rep, code := trReport(t, "--upload")
	if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadSkipped || !strings.Contains(rep.Upload.Error, "dirty") {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	if _, code := trReport(t, "--upload=required"); code != exitRefused {
		t.Fatalf("--upload=required on a dirty tree exited %d", code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("a dirty tree reached the control plane: %v", fake.calls)
	}
}

// In CI the upload is on with no flag, and a run captured there is recorded
// as a CI run, with its job.
func TestTestReportUploadsByDefaultInCI(t *testing.T) {
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
	trUploadProject(t, trStream("pass"), 0, fake, func(m *testreport.RunManifest) {
		m.CI = &testreport.ManifestCI{Provider: "github", RunID: "42", RunURL: "https://github.com/acme/shop/actions/runs/42", Attempt: 2}
	})
	rep, _ := trReport(t)
	if rep.Upload != nil || len(fake.calls) != 0 {
		t.Fatalf("uploaded outside CI with no flag: %+v %v", rep.Upload, fake.calls)
	}
	testUploadInCI = func() bool { return true }
	rep, code := trReport(t)
	if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	b := fake.recorded()[0]
	ci, _ := b["ci"].(map[string]any)
	if b["source"] != "TEST_RUN_SOURCE_CI" || ci["provider"] != "github" || ci["runId"] != "42" ||
		ci["runUrl"] != "https://github.com/acme/shop/actions/runs/42" || ci["attempt"] != float64(2) {
		t.Fatalf("CI run sent as %v ci=%v", b["source"], ci)
	}
	if _, code := trReport(t, "--upload=false"); code != exitOK || len(fake.recorded()) != 1 {
		t.Fatalf("--upload=false uploaded in CI: %d records", len(fake.recorded()))
	}
}

// With a base, the diff's changed lines go as their own part, beside the base
// sha that RecordTestRun requires of a diff.
func TestTestReportUploadSendsTheDiff(t *testing.T) {
	const base = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
	trUploadProject(t, trStream("pass"), 0, fake, nil)
	inner := testGit
	testGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		switch args[0] {
		case "rev-parse":
			if args[1] == "--verify" {
				return []byte(base + "\n"), nil
			}
		case "merge-base":
			return []byte(base + "\n"), nil
		case "diff":
			return []byte("diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -3,0 +4,1 @@\n+\treturn 1\n"), nil
		}
		return inner(ctx, dir, args...)
	}
	rep, code := trReport(t, "--upload", "--base", "main")
	if code != exitOK || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	b := fake.recorded()[0]
	format, doc := gunzipPart(t, b, "diff")
	var d testreport.Diff
	if err := json.Unmarshal(doc, &d); err != nil || format != testFormatDiff || d.Schema != testreport.SchemaDiff {
		t.Fatalf("diff part %s: %v\n%s", format, err, doc)
	}
	if b["baseSha"] != base || d.Base != base || d.Commit != trCommit || len(d.Files) != 1 || d.Files[0].Path != "a/a.go" ||
		d.Files[0].Ranges[0] != (testreport.LineRange{Start: 4, End: 4}) {
		t.Fatalf("diff: baseSha %v %+v", b["baseSha"], d)
	}
	if bytes.Contains(doc, []byte("return 1")) {
		t.Fatal("the diff part carries source")
	}
}

// B1: a lane whose coverage could not be assembled is still recorded with
// its results, but the upload says so: status partial, the coverage named as
// omitted with its reason, one warning naming the lane, and exit 4 when the
// upload was required. A failed test still exits 1.
func TestTestReportUploadWithoutCoverageIsPartial(t *testing.T) {
	for _, tc := range []struct {
		name, flag, stream string
		laneExit, want     int
	}{
		{"plain upload keeps the run's code", "--upload", trStream("pass"), 0, exitOK},
		{"required upload exits 4", "--upload=required", trStream("pass"), 0, exitRefused},
		{"a failed test outranks it", "--upload=required", trStream("fail"), 1, exitWrong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
			trUploadProject(t, tc.stream, tc.laneExit, fake, nil)
			prev := testUploadAssemble
			testUploadAssemble = func(context.Context, testlocal.Options) (*testlocal.Assembly, error) {
				return nil, &testreport.SourceMismatchError{}
			}
			t.Cleanup(func() { testUploadAssemble = prev })

			cmd := newTestReportCmd()
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			cmd.SetArgs([]string{"--json", tc.flag})
			code := exitcode.Of(cmd.Execute())
			var rep testreport.Report
			if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
				t.Fatalf("not one JSON document: %v\n%s", err, out.String())
			}
			if code != tc.want || rep.ExitCode != tc.want {
				t.Fatalf("exit %d (document %d), want %d: %+v", code, rep.ExitCode, tc.want, rep.Upload)
			}
			up := rep.Upload
			if up == nil || up.Status != testUploadPartial || up.Omitted == nil || len(*up.Omitted) != 1 {
				t.Fatalf("upload: %+v", up)
			}
			o := (*up.Omitted)[0]
			if o.Run != "01RUN" || o.Lane != "unit" || o.Part != "coverage" || !strings.Contains(o.Reason, "could not be assembled") {
				t.Fatalf("omission: %+v", o)
			}
			if up.URL == "" || !strings.Contains(up.Error, "coverage") {
				t.Fatalf("a partial upload names no run or no reason: %+v", up)
			}
			recs := fake.recorded()
			if len(recs) != 1 || recs[0]["results"] == nil || recs[0]["coverage"] != nil {
				t.Fatalf("want the results sent without coverage: %v", recs)
			}
			if n := strings.Count(errOut.String(), "warning: lane unit of run 01RUN was uploaded without its coverage"); n != 1 {
				t.Fatalf("%d warnings naming the lane, want 1:\n%s", n, errOut.String())
			}
		})
	}
}

func TestTestUploadWanted(t *testing.T) {
	for _, tc := range []struct {
		flag, configured string
		inCI             bool
		want             bool
		why              bool
	}{
		{uploadAuto, "", false, false, false},
		{uploadAuto, "", true, true, false},
		{uploadAuto, config.TestsUploadCI, true, true, false},
		{uploadAuto, config.TestsUploadAlways, false, true, false},
		{uploadAuto, config.TestsUploadNever, true, false, false},
		{uploadOn, "", false, true, false},
		{uploadRequired, config.TestsUploadCI, false, true, false},
		{uploadOn, config.TestsUploadNever, false, false, true},
		{uploadRequired, config.TestsUploadNever, true, false, true},
		{uploadOff, config.TestsUploadAlways, true, false, false},
	} {
		want, why := testUploadWanted(tc.flag, tc.configured, tc.inCI)
		if want != tc.want || (why != "") != tc.why {
			t.Errorf("flag %q tests.upload %q CI %v: want=%v why=%q", tc.flag, tc.configured, tc.inCI, want, why)
		}
	}
}

// A part over 1 MiB must be sent gzip-encoded (RecordTestRun refuses it
// otherwise): every part is, and it decodes to the document's own bytes.
func TestTestReportUploadGzipsALargeResultsFile(t *testing.T) {
	var big strings.Builder
	big.WriteString(`{"Action":"run","Package":"example.com/m/a","Test":"TestF"}` + "\n")
	line := `{"Action":"output","Package":"example.com/m/a","Test":"TestF","Output":"` + strings.Repeat("x", 200) + `\n"}` + "\n"
	for big.Len() < 3<<20 {
		big.WriteString(line)
	}
	big.WriteString(`{"Action":"pass","Package":"example.com/m/a","Test":"TestF","Elapsed":0}` + "\n")
	big.WriteString(`{"Action":"pass","Package":"example.com/m/a","Elapsed":0.1}` + "\n")
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
	trUploadProject(t, big.String(), 0, fake, nil)
	rep, code := trReport(t, "--upload=required")
	if code != exitOK || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	b := fake.recorded()[0]
	raw, _ := base64.StdEncoding.DecodeString(b["results"].(map[string]any)["data"].(string))
	if len(raw) >= 1<<20 {
		t.Fatalf("the results part is %d bytes as sent", len(raw))
	}
	if _, doc := gunzipPart(t, b, "results"); doc == nil || len(doc) != big.Len() {
		t.Fatalf("results part decodes to %d bytes, want %d", len(doc), big.Len())
	}
}
