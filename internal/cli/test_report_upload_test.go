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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/credentials"
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
	// recordBearer, when set, is the only bearer RecordTestRun accepts:
	// any other is refused 403 for missing tests:write, as the control
	// plane refuses a credential carrying neither upload scope.
	recordBearer string
	// exchangeGrants maps a bearer to the token ExchangeToken hands it for
	// exactly [tests:write]; any other exchange is refused 403. exchanges
	// are the scopes each exchange asked for.
	exchangeGrants map[string]string
	exchanges      [][]string
	// bearers are the bearers RecordTestRun was called with, in order.
	bearers []string
	// exchange is how AccessTokenService/ExchangeFederatedToken answers:
	// "ok" mints rlat_oidc-minted, "refuse" is FailedPrecondition with
	// trOIDCRemedy, and "" is not served (404 unimplemented, an older
	// control plane).
	exchange string
}

const trOIDCRemedy = "install the Reliant GitHub App on acme/shop, then link it in Organization settings"

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
	case "/controlplane.v1.AccessTokenService/ExchangeToken":
		var req struct {
			Scopes []string `json:"scopes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.exchanges = append(f.exchanges, req.Scopes)
		granted, ok := f.exchangeGrants[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok || len(req.Scopes) != 1 || req.Scopes[0] != testUploadScope {
			connectErr(http.StatusForbidden, "permission_denied", "an exchange cannot widen a credential")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secret": granted,
			"token":  map[string]any{"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		})
	case "/controlplane.v1.TestsService/RecordTestRun":
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.bearers = append(f.bearers, bearer)
		if f.recordBearer != "" && bearer != f.recordBearer {
			connectErr(http.StatusForbidden, "permission_denied", "this machine credential does not carry the deploy:write or tests:write scope")
			return
		}
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
	case "/controlplane.v1.AccessTokenService/ExchangeFederatedToken":
		switch f.exchange {
		case "ok":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "rlat_oidc-minted", "expiresAt": "2999-01-01T00:00:00Z", "scopes": []string{"tests:write"}})
		case "refuse":
			connectErr(http.StatusBadRequest, cloud.CodeFailedPrecondition, trOIDCRemedy)
		default:
			connectErr(http.StatusNotFound, cloud.CodeUnimplemented, "no "+r.URL.Path)
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
		t.Fatalf("not one JSON document (exit %d): %v\n%s", code, err, out)
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

// signInAs replaces trUploadProject's token variable with a stored `forge
// login` for the fake control plane: a session's credential, which forge may
// elevate, where a token handed to it verbatim is presented as it is. The
// credential helper (a Reliant daemon's session) is elevated the same way
// (cloud.Client.elevatable).
func signInAs(t *testing.T, token string) {
	t.Helper()
	t.Setenv(cloud.DefaultTokenEnv, "")
	decl, err := testUploadControlPlane(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path, err := cloud.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Store(path, decl.Endpoint, cloud.ClientID, credentials.Credential{Token: token, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// A session's credential is minted for forge's login scopes, which carry
// deploy:write only for a person granted it. The upload elevates for the
// narrow tests:write before its first RecordTestRun, so a session whose
// person holds tests:write alone records runs, and the control plane refuses
// nothing.
func TestTestReportUploadElevatesASessionForTestsWrite(t *testing.T) {
	fake := &fakeTestsService{
		features:       []string{hostedTestFeatureResults},
		recordBearer:   "rlat_tests_write",
		exchangeGrants: map[string]string{"rlat_session": "rlat_tests_write"},
	}
	trUploadProject(t, trStream("pass"), 0, fake, nil)
	signInAs(t, "rlat_session")
	rep, code := trReport(t, "--upload=required")
	if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	if want := [][]string{{testUploadScope}}; !reflect.DeepEqual(fake.exchanges, want) {
		t.Fatalf("exchanges %v, want %v", fake.exchanges, want)
	}
	if want := []string{"rlat_tests_write"}; !reflect.DeepEqual(fake.bearers, want) {
		t.Fatalf("RecordTestRun bearers %v, want the elevated token alone %v", fake.bearers, want)
	}
}

// A session whose person cannot be elevated for tests:write (it holds
// deploy:write, which RecordTestRun also accepts, or the control plane
// predates the scope) still uploads with its own credential, as it did before
// the scope was declared.
func TestTestReportUploadFallsBackToTheSessionWhenElevationIsRefused(t *testing.T) {
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}, recordBearer: "rlat_session"}
	trUploadProject(t, trStream("pass"), 0, fake, nil)
	signInAs(t, "rlat_session")
	rep, code := trReport(t, "--upload=required")
	if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	if len(fake.exchanges) != 1 {
		t.Fatalf("exchanges %v, want the one refused tests:write exchange", fake.exchanges)
	}
	if want := []string{"rlat_session"}; !reflect.DeepEqual(fake.bearers, want) {
		t.Fatalf("RecordTestRun bearers %v, want %v", fake.bearers, want)
	}
}

// An explicit token (FORGE_CONTROL_PLANE_TOKEN: a CI secret, or
// ci-full-main.sh's tests:write token) is presented verbatim and never traded.
func TestTestReportUploadPresentsAnExplicitTokenAsIs(t *testing.T) {
	fake := &fakeTestsService{
		features:       []string{hostedTestFeatureResults},
		exchangeGrants: map[string]string{"rlat_test": "rlat_tests_write"},
	}
	trUploadProject(t, trStream("pass"), 0, fake, nil)
	rep, code := trReport(t, "--upload=required")
	if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
		t.Fatalf("exit %d upload %+v", code, rep.Upload)
	}
	if len(fake.exchanges) != 0 {
		t.Fatalf("an explicit token was exchanged: %v", fake.exchanges)
	}
	if want := []string{"rlat_test"}; !reflect.DeepEqual(fake.bearers, want) {
		t.Fatalf("RecordTestRun bearers %v, want %v", fake.bearers, want)
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

// The upload carries forge.yaml's tests.flakes (control-plane#3434's
// flake_policy) and the root go.mod's module path when the project has them,
// and neither field otherwise.
func TestTestReportUploadCarriesFlakePolicyAndGoModule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		forgeYML string // "" writes no forge.yaml
		noGoMod  bool
		policy   map[string]any // nil: no flakePolicy sent
		module   string         // "": no goModule sent
	}{
		{"issues and pr", "name: shop\nmodule_path: example.com/m\ntests:\n  flakes:\n    issues: true\n    quarantine: pr\n", false,
			map[string]any{"issues": true, "quarantine": "TEST_QUARANTINE_MODE_PR"}, "example.com/m"},
		{"suggest only", "name: shop\nmodule_path: example.com/m\ntests:\n  flakes:\n    quarantine: suggest\n", false,
			map[string]any{"quarantine": "TEST_QUARANTINE_MODE_SUGGEST"}, "example.com/m"},
		{"no tests.flakes", "name: shop\nmodule_path: example.com/m\n", false, nil, "example.com/m"},
		{"no forge.yaml and no go.mod", "", true, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
			root := trUploadProject(t, trStream("pass"), 0, fake, nil)
			if tc.forgeYML != "" {
				trWrite(t, filepath.Join(root, "forge.yaml"), tc.forgeYML)
			}
			if tc.noGoMod {
				if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
					t.Fatal(err)
				}
			}
			rep, code := trReport(t, "--upload")
			if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
				t.Fatalf("exit %d upload %+v", code, rep.Upload)
			}
			b := fake.recorded()[0]
			got, sent := b["flakePolicy"]
			switch {
			case tc.policy == nil && sent:
				t.Errorf("flakePolicy sent with none configured: %v", got)
			case tc.policy != nil && !reflect.DeepEqual(got, tc.policy):
				t.Errorf("flakePolicy = %v, want %v", got, tc.policy)
			}
			mod, sent := b["goModule"]
			switch {
			case tc.module == "" && sent:
				t.Errorf("goModule sent with no go.mod: %v", mod)
			case tc.module != "" && mod != tc.module:
				t.Errorf("goModule = %v, want %s", mod, tc.module)
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

// trOIDCJob makes this process a GitHub Actions job granted `id-token: write`
// with no FORGE_CONTROL_PLANE_TOKEN: the upload's only credential is the job's
// OIDC token ("idtok-job"), exchanged on fake's control plane.
func trOIDCJob(t *testing.T) {
	t.Helper()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": "idtok-job"})
	}))
	t.Cleanup(tokens.Close)
	t.Setenv(cloud.DefaultTokenEnv, "")
	t.Setenv(cloud.CIOIDCEnv, "")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tokens.URL+"/?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "actions-request-bearer")
}

// In a GitHub Actions job with no secret, the upload is authorized by the
// job's own identity, and neither token reaches the report.
func TestTestReportUploadWithGitHubOIDC(t *testing.T) {
	fake := &fakeTestsService{features: []string{hostedTestFeatureResults}, exchange: "ok"}
	trUploadProject(t, trStream("pass"), 0, fake, nil)
	trOIDCJob(t)
	out, code := trRun(t, newTestReportCmd(), "--json", "--upload=required")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	fake.mu.Lock()
	bearers, exchanges := append([]string(nil), fake.bearers...), len(fake.exchanges)
	fake.mu.Unlock()
	if want := []string{"rlat_oidc-minted"}; !reflect.DeepEqual(bearers, want) {
		t.Fatalf("RecordTestRun bearers %v, want the minted token %v", bearers, want)
	}
	if exchanges != 0 {
		t.Fatalf("the CI-identity token was traded through ExchangeToken (%d exchanges)", exchanges)
	}
	for _, secret := range []string{"rlat_oidc-minted", "idtok-job", "actions-request-bearer"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the report carries %q:\n%s", secret, out)
		}
	}
}

// A control plane that refuses the exchange, or predates it, leaves the upload
// skipped and green; --upload=required makes that exit 4, naming the remedy.
func TestTestReportUploadRefusedOIDCExchange(t *testing.T) {
	for _, exchange := range []string{"", "refuse"} {
		t.Run("exchange="+exchange, func(t *testing.T) {
			fake := &fakeTestsService{features: []string{hostedTestFeatureResults}, exchange: exchange}
			trUploadProject(t, trStream("pass"), 0, fake, nil)
			trOIDCJob(t)
			rep, code := trReport(t, "--upload")
			if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadSkipped {
				t.Fatalf("exit %d upload %+v", code, rep.Upload)
			}
			rep, code = trReport(t, "--upload=required")
			if code != exitRefused || rep.ExitCode != exitRefused {
				t.Fatalf("--upload=required exited %d (document %d): %+v", code, rep.ExitCode, rep.Upload)
			}
			if exchange == "refuse" && !strings.Contains(rep.Upload.Error, trOIDCRemedy) {
				t.Fatalf("the skip does not name the control plane's remedy: %s", rep.Upload.Error)
			}
			if len(fake.recorded()) != 0 {
				t.Fatal("a refused exchange still recorded a run")
			}
		})
	}
}

// In CI the job's own identity outranks a login file a runner happens to hold
// (on a self-hosted runner, a person's): the OIDC token is exchanged and
// presented as it is, and the session is neither used nor elevated. A refused
// exchange does not fall back to that login either.
func TestTestReportUploadOIDCBeatsARunnersLogin(t *testing.T) {
	for _, exchange := range []string{"ok", "refuse"} {
		t.Run("exchange="+exchange, func(t *testing.T) {
			fake := &fakeTestsService{
				features:       []string{hostedTestFeatureResults},
				exchange:       exchange,
				exchangeGrants: map[string]string{"rlat_session": "rlat_tests_write"},
			}
			trUploadProject(t, trStream("pass"), 0, fake, nil)
			signInAs(t, "rlat_session")
			trOIDCJob(t)
			rep, code := trReport(t, "--upload=required")
			fake.mu.Lock()
			bearers, exchanges := append([]string(nil), fake.bearers...), len(fake.exchanges)
			fake.mu.Unlock()
			if exchanges != 0 {
				t.Fatalf("the runner's login was elevated (%d exchanges)", exchanges)
			}
			if exchange == "refuse" {
				if code != exitRefused || len(bearers) != 0 {
					t.Fatalf("a refused exchange fell back to the login: exit %d bearers %v", code, bearers)
				}
				return
			}
			if code != exitOK || rep.Upload == nil || rep.Upload.Status != testUploadOK {
				t.Fatalf("exit %d upload %+v", code, rep.Upload)
			}
			if want := []string{"rlat_oidc-minted"}; !reflect.DeepEqual(bearers, want) {
				t.Fatalf("RecordTestRun bearers %v, want the job's token %v", bearers, want)
			}
		})
	}
}
