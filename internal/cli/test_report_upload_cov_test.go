package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/testreport"
)

const covASha = "0123456789abcdef0123456789abcdef01234567"

// covAUploadRun is a run with one lane directory on disk, ready for
// testUploadRequest. results is the lane's results file ("" writes none).
func covAUploadRun(t *testing.T, format testreport.LaneFormat, results string, edit func(*testlocal.Run, *testlocal.Lane)) (*testlocal.Run, *testlocal.Lane, testUploadInput) {
	t.Helper()
	root := t.TempDir()
	laneDir := filepath.Join(root, "run", "unit")
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lane := testlocal.Lane{Dir: laneDir}
	lane.Lane, lane.Format, lane.ExitCode = "unit", format, 3
	lane.StartedAt = time.Date(2026, 11, 1, 10, 0, 0, 0, time.FixedZone("x", 3600))
	lane.FinishedAt = lane.StartedAt.Add(time.Minute)
	lane.Results = testreport.LaneResultsParsed
	if results != "" {
		lane.ResultsFile = "results.json"
		if err := os.WriteFile(filepath.Join(laneDir, "results.json"), []byte(results), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		lane.Results = testreport.LaneResultsMissing
	}
	run := &testlocal.Run{Dir: filepath.Join(root, "run"), Lanes: []testlocal.Lane{lane}}
	run.ID, run.Commit, run.TreeHash, run.Branch = "01RUN", strings.ToUpper(covASha), "", "feat"
	if edit != nil {
		edit(run, &run.Lanes[0])
	}
	prev := testGit
	testGit = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" && strings.HasSuffix(args[len(args)-1], "^{tree}") {
			return []byte("fedcba9876543210fedcba9876543210fedcba98\n"), nil
		}
		return nil, errors.New("no git")
	}
	t.Cleanup(func() { testGit = prev })
	return run, &run.Lanes[0], testUploadInput{root: root, project: "shop", goModule: "example.com/shop"}
}

func covAGunzip(t *testing.T, p *wireTestUploadPart) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(p.Data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTestUploadRequestBuildsTheWireRequest(t *testing.T) {
	run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, `{"Action":"pass"}`+"\n", func(r *testlocal.Run, l *testlocal.Lane) {
		r.CI = &testreport.ManifestCI{Provider: "github", RunID: "99", RunURL: "https://github.com/acme/shop/actions/runs/99", Attempt: -4}
		r.BaseSHA = strings.ToUpper("1111111111111111111111111111111111111111")
		r.ForgeVersion = strings.Repeat("v", 200)
	})
	in.flakePolicy = &wireTestFlakePolicy{Issues: true}
	req, omitted, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
	if err != nil || skip != "" || len(omitted) != 0 {
		t.Fatalf("err=%v skip=%q omitted=%v", err, skip, omitted)
	}
	checks := map[string]bool{
		"commit lower-cased":              req.GitCommit == covASha,
		"tree resolved from the commit":   req.TreeHash == "fedcba9876543210fedcba9876543210fedcba98",
		"project, lane, branch":           req.Project == "shop" && req.Lane == "unit" && req.Branch == "feat",
		"exit code of the lane":           req.ExitCode == 3,
		"CI source":                       req.Source == testSourceCI,
		"CI identity, attempt clamped":    req.CI != nil && req.CI.Provider == "github" && req.CI.RunID == "99" && req.CI.Attempt == 0,
		"CI url kept when https":          req.CI != nil && req.CI.RunURL == "https://github.com/acme/shop/actions/runs/99",
		"base sha from the manifest":      req.BaseSHA == "1111111111111111111111111111111111111111",
		"forge version capped at 128":     len(req.ForgeVersion) == 128,
		"go module and flake policy":      req.GoModule == "example.com/shop" && req.FlakePolicy != nil && req.FlakePolicy.Issues,
		"timestamps are UTC":              req.StartedAt != nil && req.StartedAt.Location() == time.UTC && req.StartedAt.Hour() == 9,
		"finish after start":              req.FinishedAt != nil && req.FinishedAt.Sub(*req.StartedAt) == time.Minute,
		"results are the file, gzipped":   req.Results != nil && req.Results.Format == "go-json" && covAGunzip(t, req.Results) == `{"Action":"pass"}`+"\n",
		"no coverage part without inputs": req.Coverage == nil,
		"no diff part without a diff":     req.Diff == nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s: request = %+v", name, req)
		}
	}
}

func TestTestUploadRequestDefaultsAndSanitisesIdentity(t *testing.T) {
	run, lane, in := covAUploadRun(t, testreport.LaneFormatJUnit, "<testsuites/>", func(r *testlocal.Run, l *testlocal.Lane) {
		r.TreeHash = strings.ToUpper(covASha)
		r.Branch = strings.Repeat("b", 300)
		r.BaseSHA = "not-a-sha"
		r.CI = &testreport.ManifestCI{Provider: "buildkite", RunID: "7", RunURL: "javascript:alert(1)"}
		l.StartedAt, l.FinishedAt = time.Time{}, time.Time{}
	})
	req, _, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
	if err != nil || skip != "" {
		t.Fatalf("err=%v skip=%q", err, skip)
	}
	if req.TreeHash != covASha {
		t.Errorf("a recorded tree hash is used as-is, lower-cased: %q", req.TreeHash)
	}
	if req.Branch != "" {
		t.Errorf("a branch over 256 bytes is dropped, not truncated: %q", req.Branch)
	}
	if req.BaseSHA != "" {
		t.Errorf("a base that is no full sha is not sent: %q", req.BaseSHA)
	}
	if req.CI == nil || req.CI.RunURL != "" {
		t.Errorf("a non-http CI url must never be sent: %+v", req.CI)
	}
	if req.ForgeVersion != buildinfo.Version() {
		t.Errorf("a run without a forge version sends this binary's: %q", req.ForgeVersion)
	}
	if req.StartedAt != nil || req.FinishedAt != nil {
		t.Errorf("unset times are omitted: %v %v", req.StartedAt, req.FinishedAt)
	}
	if req.Results.Format != "junit" {
		t.Errorf("results format = %q", req.Results.Format)
	}
}

func TestTestUploadRequestSendsTheDiffAsItsOwnPart(t *testing.T) {
	run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}\n", nil)
	diff := &testreport.Diff{Base: "1111111111111111111111111111111111111111", Commit: covASha,
		Files: []testreport.ChangedFile{{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 3, End: 4}}}}}
	req, _, _, err := testUploadRequest(context.Background(), in, run, lane, diff)
	if err != nil {
		t.Fatal(err)
	}
	if req.BaseSHA != diff.Base || req.Diff == nil || req.Diff.Format != testFormatDiff {
		t.Fatalf("request = %+v", req)
	}
	if doc := covAGunzip(t, req.Diff); !strings.Contains(doc, `"a/a.go"`) || !strings.Contains(doc, `"base":"`+diff.Base+`"`) {
		t.Errorf("diff document = %s", doc)
	}
}

func TestTestUploadRequestSkipsWhatItCannotIdentify(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testlocal.Run, *testlocal.Lane)
		want string
	}{
		{"no commit", func(r *testlocal.Run, _ *testlocal.Lane) { r.Commit = "" }, "the run recorded no full commit sha"},
		{"abbreviated commit", func(r *testlocal.Run, _ *testlocal.Lane) { r.Commit = "abcdef1" }, "the run recorded no full commit sha"},
		{"no tree and git cannot resolve one", func(r *testlocal.Run, _ *testlocal.Lane) { r.Commit = covASha }, ""},
		{"no results file", func(_ *testlocal.Run, l *testlocal.Lane) {
			l.Results, l.ResultsFile = testreport.LaneResultsMissing, ""
		}, "the lane captured no results file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}\n", tc.edit)
			if tc.name == "no tree and git cannot resolve one" {
				testGit = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not a git checkout") }
				tc.want = "the run recorded no full tree hash"
			}
			req, _, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
			if err != nil || req != nil || skip != tc.want {
				t.Errorf("req=%v skip=%q err=%v, want skip %q", req, skip, err, tc.want)
			}
		})
	}
}

func TestTestUploadRequestRefusesDocumentsTheControlPlaneWouldRefuse(t *testing.T) {
	t.Run("results file over the inflated bound", func(t *testing.T) {
		if testing.Short() || runtime.GOOS == "windows" {
			t.Skip("reads a 256 MiB sparse file (Windows does not make it sparse); runs in task test")
		}
		run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}", nil)
		f, err := os.OpenFile(filepath.Join(lane.Dir, "results.json"), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxTestUploadInflated + 1); err != nil {
			t.Fatalf("make the oversized results file: %v", err)
		}
		_ = f.Close()
		_, _, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
		if err == nil || skip != "" || !strings.Contains(err.Error(), "the results file is over") {
			t.Fatalf("skip=%q err=%v", skip, err)
		}
	})
	t.Run("an empty results file is skipped, not uploaded", func(t *testing.T) {
		run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}", nil)
		if err := os.WriteFile(filepath.Join(lane.Dir, "results.json"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
		if err != nil || skip != "the lane's results file is empty" {
			t.Fatalf("skip=%q err=%v", skip, err)
		}
	})
}

func TestTestUploadRequestNamesCoverageItCouldNotSend(t *testing.T) {
	t.Run("unreadable coverage", func(t *testing.T) {
		run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}\n", func(_ *testlocal.Run, l *testlocal.Lane) {
			l.Coverage = []testreport.LaneCoverageInput{{Path: "cover.out", Format: testreport.CoverageGoProfile}}
		})
		if err := os.WriteFile(filepath.Join(lane.Dir, "cover.out"), []byte("this is not a profile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		req, omitted, skip, err := testUploadRequest(context.Background(), in, run, lane, nil)
		if err != nil || skip != "" || req == nil {
			t.Fatalf("req=%v skip=%q err=%v: the results are still worth recording", req, skip, err)
		}
		if req.Coverage != nil {
			t.Error("no coverage part may be sent")
		}
		if len(omitted) != 1 || omitted[0].Part != "coverage" || omitted[0].Run != "01RUN" || omitted[0].Lane != "unit" || omitted[0].Reason == "" {
			t.Errorf("omitted = %+v", omitted)
		}
	})
	t.Run("assembly failure", func(t *testing.T) {
		run, lane, in := covAUploadRun(t, testreport.LaneFormatGoJSON, "{}\n", func(_ *testlocal.Run, l *testlocal.Lane) {
			l.Coverage = []testreport.LaneCoverageInput{{Path: "cover.out", Format: testreport.CoverageGoProfile}}
		})
		prev := testUploadAssemble
		testUploadAssemble = func(context.Context, testlocal.Options) (*testlocal.Assembly, error) {
			return nil, errors.New("disk on fire\nsecond line")
		}
		t.Cleanup(func() { testUploadAssemble = prev })
		_, omitted, _, err := testUploadRequest(context.Background(), in, run, lane, nil)
		if err != nil || len(omitted) != 1 || omitted[0].Reason != "its coverage could not be assembled: disk on fire" {
			t.Fatalf("err=%v omitted=%+v", err, omitted)
		}
	})
}

func TestTestUploadCoverageOmitted(t *testing.T) {
	withInputs := &testlocal.Lane{}
	withInputs.Coverage = []testreport.LaneCoverageInput{{Path: "cover.out", Format: testreport.CoverageGoProfile}}
	none := &testlocal.Lane{Dir: t.TempDir()}
	tests := []struct {
		name string
		l    *testlocal.Lane
		la   *testlocal.Assembly
		err  error
		want string
	}{
		{"no coverage captured is nothing to omit", none, nil, nil, ""},
		{"assembled", withInputs, &testlocal.Assembly{Coverage: &testreport.Coverage{}}, nil, ""},
		{"assembly failed", withInputs, nil, errors.New("bad profile\ndetail"), "its coverage could not be assembled: bad profile"},
		{"files held nothing", withInputs, &testlocal.Assembly{}, nil, "its coverage files held nothing forge could read"},
		{"files held nothing, with a note", withInputs, &testlocal.Assembly{Notes: []string{"cover.out: empty\nmore"}}, nil, "its coverage files held nothing forge could read (cover.out: empty)"},
		{"no assembly at all", withInputs, nil, nil, "its coverage files held nothing forge could read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := testUploadCoverageOmitted(tc.l, tc.la, tc.err); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTestUploadResultsFallsBackToTheNormalisedDocument(t *testing.T) {
	run, lane, _ := covAUploadRun(t, testreport.LaneFormatUnknown, "plain text", nil)
	_ = run
	t.Run("a format the control plane cannot read raw, with no assembly", func(t *testing.T) {
		_, _, why, err := testUploadResults(lane, nil)
		if err != nil || why != "the lane's results are in no format forge reads (unknown)" {
			t.Fatalf("why=%q err=%v", why, err)
		}
	})
	t.Run("an empty format is a dash", func(t *testing.T) {
		l := *lane
		l.Format = ""
		_, _, why, _ := testUploadResults(&l, &testlocal.Assembly{})
		if !strings.HasSuffix(why, "(-)") {
			t.Errorf("why = %q", why)
		}
	})
	t.Run("the assembled results are sent as forge's document", func(t *testing.T) {
		la := &testlocal.Assembly{Results: []*testreport.Results{{Schema: testreport.SchemaResults}}}
		b, format, why, err := testUploadResults(lane, la)
		if err != nil || why != "" || format != testFormatResults || !strings.Contains(string(b), testreport.SchemaResults) {
			t.Fatalf("b=%s format=%q why=%q err=%v", b, format, why, err)
		}
	})
	t.Run("a results path that vanished between stat and open", func(t *testing.T) {
		l := *lane
		l.Format = testreport.LaneFormatGoJSON
		l.Dir = t.TempDir()
		l.ResultsFile = "results.json"
		if err := os.Mkdir(filepath.Join(l.Dir, "results.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, why, err := testUploadResults(&l, nil)
		if why != "the lane captured no results file" || err != nil {
			t.Fatalf("a directory is not a results file: why=%q err=%v", why, err)
		}
	})
}

func TestTestUploadDiffNeedsAFullBaseSha(t *testing.T) {
	changed := []testreport.ChangedFile{{Path: "a.go"}}
	full := "1111111111111111111111111111111111111111"
	sha256Form := strings.Repeat("a", 64)
	tests := []struct {
		name string
		a    testlocal.Assembly
		want bool
	}{
		{"no diff computed", testlocal.Assembly{BaseSHA: full}, false},
		{"abbreviated base", testlocal.Assembly{Changed: changed, BaseSHA: "1111111"}, false},
		{"uppercase base", testlocal.Assembly{Changed: changed, BaseSHA: strings.ToUpper(covASha)}, false},
		{"sha1 base", testlocal.Assembly{Changed: changed, BaseSHA: full, Commit: covASha}, true},
		{"sha256 base", testlocal.Assembly{Changed: changed, BaseSHA: sha256Form}, true},
		{"a computed, empty diff is still a diff", testlocal.Assembly{Changed: []testreport.ChangedFile{}, BaseSHA: full}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := testUploadDiff(&tc.a)
			if (d != nil) != tc.want {
				t.Fatalf("diff = %+v, want present=%v", d, tc.want)
			}
			if d != nil && (!d.OK || d.Schema != testreport.SchemaDiff || d.Base != tc.a.BaseSHA || d.Commit != tc.a.Commit) {
				t.Errorf("diff = %+v", d)
			}
		})
	}
}

func TestTestGitLineTrimsAndPropagatesErrors(t *testing.T) {
	prev := testGit
	t.Cleanup(func() { testGit = prev })
	testGit = func(_ context.Context, dir string, args ...string) ([]byte, error) {
		if dir != "/repo" || strings.Join(args, " ") != "rev-parse HEAD" {
			t.Errorf("git called as %q %v", dir, args)
		}
		return []byte("  abc123\n"), nil
	}
	if got, err := testGitLine(context.Background(), "/repo", "rev-parse", "HEAD"); err != nil || got != "abc123" {
		t.Errorf("got %q err %v", got, err)
	}
	testGit = func(context.Context, string, ...string) ([]byte, error) { return []byte("ignored"), errors.New("boom") }
	if got, err := testGitLine(context.Background(), "/repo"); err == nil || got != "" {
		t.Errorf("got %q err %v: output of a failed git is never used", got, err)
	}
}

func TestTestUploadNote(t *testing.T) {
	ok := func(status string) testUploadRun { return testUploadRun{Status: status} }
	tests := []struct {
		name string
		up   testUpload
		want string
	}{
		{"uploaded with a link", testUpload{Upload: testreport.Upload{Status: testUploadOK, URL: "https://c/t/1"}, Runs: []testUploadRun{ok(testUploadOK), ok(testUploadPartial), ok(testUploadSkipped)}},
			"forge test report: uploaded 2 lane run(s): https://c/t/1"},
		{"uploaded with no link", testUpload{Upload: testreport.Upload{Status: testUploadOK}, Runs: []testUploadRun{ok(testUploadOK)}},
			"forge test report: uploaded 1 lane run(s)"},
		{"failed", testUpload{Upload: testreport.Upload{Status: testUploadFailed, Error: "HTTP 500"}},
			"forge test report: upload failed: HTTP 500; the report stays in .forge/tests/"},
		{"skipped", testUpload{Upload: testreport.Upload{Status: testUploadSkipped, Error: "dirty"}},
			"forge test report: upload skipped: dirty; the report stays in .forge/tests/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := testUploadNote(&tc.up); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestTestUploadVerdictAggregatesLaneRuns(t *testing.T) {
	console := cloud.NewConsole("https://console.test", "shop")
	om := func(lane string) testreport.UploadOmission {
		return testreport.UploadOmission{Run: "R", Lane: testreport.Lane(lane), Part: "coverage", Reason: "bad"}
	}
	okRun := func(url string) testUploadRun { return testUploadRun{Status: testUploadOK, URL: url} }
	tests := []struct {
		name                         string
		runs                         []testUploadRun
		ok, partial, failed          int
		firstErr, why                string
		wantStatus, wantErr, wantURL string
		wantOmitted                  int
	}{
		{name: "one lane recorded: its own url", runs: []testUploadRun{okRun("https://c/1")}, ok: 1,
			wantStatus: testUploadOK, wantURL: "https://c/1"},
		{name: "several lanes recorded: the tests index", runs: []testUploadRun{okRun("https://c/1"), okRun("https://c/2")}, ok: 2,
			wantStatus: testUploadOK, wantURL: console.Tests()},
		{name: "a failure outranks successes and counts the rest", runs: []testUploadRun{okRun("u")}, ok: 1, failed: 3, firstErr: "lane unit of run R: HTTP 500",
			wantStatus: testUploadFailed, wantErr: "lane unit of run R: HTTP 500 (and 2 more)", wantURL: "u"},
		{name: "partial names its first omission and counts the others",
			runs: []testUploadRun{{Status: testUploadPartial, URL: "p", Omitted: []testreport.UploadOmission{om("unit"), om("e2e")}}}, ok: 1, partial: 1,
			wantStatus: testUploadPartial, wantErr: "lane unit of run R was recorded without its coverage: bad (and 1 more omitted part(s))", wantURL: "p", wantOmitted: 2},
		{name: "partial is not reported once the control plane stopped serving runs",
			runs: []testUploadRun{{Status: testUploadPartial, Omitted: []testreport.UploadOmission{om("unit")}}}, ok: 1, partial: 1, why: "not stored",
			wantStatus: testUploadSkipped, wantErr: "not stored", wantOmitted: 1},
		{name: "nothing recorded: the first lane's reason", runs: []testUploadRun{{Status: testUploadSkipped, Error: "no tree"}},
			wantStatus: testUploadSkipped, wantErr: "no tree"},
		{name: "nothing recorded and no reason", runs: nil, wantStatus: testUploadSkipped, wantErr: "the report has no lane run to upload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := &testUpload{Runs: tc.runs}
			got := testUploadVerdict(up, tc.ok, tc.partial, tc.failed, tc.firstErr, tc.why, console)
			if got.Status != tc.wantStatus || got.Error != tc.wantErr || got.URL != tc.wantURL || len(got.omitted()) != tc.wantOmitted {
				t.Errorf("status=%q err=%q url=%q omitted=%d\nwant     %q err=%q url=%q omitted=%d",
					got.Status, got.Error, got.URL, len(got.omitted()), tc.wantStatus, tc.wantErr, tc.wantURL, tc.wantOmitted)
			}
		})
	}
}

func TestTestGoModule(t *testing.T) {
	dir := t.TempDir()
	if got := testGoModule(dir); got != "" {
		t.Errorf("no go.mod: %q", got)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("module example.com/shop\n\ngo 1.24\n")
	if got := testGoModule(dir); got != "example.com/shop" {
		t.Errorf("got %q", got)
	}
	write("module " + strings.Repeat("m", maxTestGoModule+1) + "\n")
	if got := testGoModule(dir); got != "" {
		t.Errorf("a module path RecordTestRun would refuse is not sent: %d bytes", len(got))
	}
	write("not a go.mod\n")
	if got := testGoModule(dir); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestProjectTestControlPlane(t *testing.T) {
	envDoc := func(t *testing.T, dir, env, cp string) {
		t.Helper()
		if err := writeMonitoringDocument(filepath.Join(dir, "deploy", "envs", env+".yaml"), "shop", env, config.MonitoringConfig{Enabled: true, ControlPlane: cp}); err != nil {
			t.Fatal(err)
		}
	}
	project := func(t *testing.T, forgeYAML string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		return dir
	}

	t.Run("outside a project it is Reliant's", func(t *testing.T) {
		t.Chdir(t.TempDir())
		d, err := projectTestControlPlane(context.Background())
		if err != nil || d.Endpoint != cloud.DefaultEndpoint {
			t.Fatalf("d=%+v err=%v", d, err)
		}
	})
	t.Run("a project whose envs declare none is Reliant's", func(t *testing.T) {
		project(t, "name: shop\nmodule_path: example.com/shop\n")
		d, err := projectTestControlPlane(context.Background())
		if err != nil || d.Endpoint != cloud.DefaultEndpoint {
			t.Fatalf("d=%+v err=%v", d, err)
		}
	})
	t.Run("one env document names the control plane", func(t *testing.T) {
		dir := project(t, "name: shop\nmodule_path: example.com/shop\n")
		envDoc(t, dir, "prod", "https://cp.example.test/")
		d, err := projectTestControlPlane(context.Background())
		if err != nil || d.Endpoint != "https://cp.example.test" {
			t.Fatalf("d=%+v err=%v", d, err)
		}
	})
	t.Run("envs on different control planes are refused, not guessed", func(t *testing.T) {
		dir := project(t, "name: shop\nmodule_path: example.com/shop\n")
		envDoc(t, dir, "prod", "https://a.example.test")
		envDoc(t, dir, "staging", "https://b.example.test")
		_, err := projectTestControlPlane(context.Background())
		if err == nil || !strings.Contains(err.Error(), "declare 2 control planes (https://a.example.test, https://b.example.test)") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("two spellings of one endpoint are one control plane", func(t *testing.T) {
		dir := project(t, "name: shop\nmodule_path: example.com/shop\n")
		envDoc(t, dir, "prod", "https://a.example.test")
		envDoc(t, dir, "staging", "https://A.example.test/")
		d, err := projectTestControlPlane(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(strings.TrimRight(d.Endpoint, "/"), "https://a.example.test") {
			t.Errorf("endpoint = %q", d.Endpoint)
		}
	})
}

func TestTestUploadWithNoRunsReportsNothingToUpload(t *testing.T) {
	var out bytes.Buffer
	rep := &testreport.Report{}
	trNoControlPlane(t)
	t.Chdir(t.TempDir())
	err := applyTestUpload(context.Background(), &out, rep, uploadOff, testUploadInput{})
	if err != nil || rep.Upload != nil || out.Len() != 0 {
		t.Errorf("--upload=off with nothing configured is silent: err=%v upload=%+v out=%q", err, rep.Upload, out.String())
	}
	rep = &testreport.Report{}
	if err := os.WriteFile("forge.yaml", []byte("name: shop\nmodule_path: example.com/shop\ntests:\n  upload: never\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyTestUpload(context.Background(), &out, rep, uploadOn, testUploadInput{}); err != nil {
		t.Fatal(err)
	}
	if rep.Upload == nil || rep.Upload.Status != testUploadSkipped || !strings.Contains(out.String(), "forge.yaml sets tests.upload: never") {
		t.Errorf("an explicit --upload against tests.upload: never must say why it did nothing: %+v %q", rep.Upload, out.String())
	}
}
