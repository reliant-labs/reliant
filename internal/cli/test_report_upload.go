package cli

// `forge test report --upload[=required]`: each clean-tree lane run of the
// report, recorded on the project's control plane with RecordTestRun
// (hosted_tests.go). Design: docs/proposals/tests-coverage-flakes.md §1.6
// (where it runs) and §1.7 (exit codes).
//
// NEVER BLOCKS. An upload that cannot happen, or fails, is one line on stderr
// and the document's `upload` field; it never changes the tests' verdict.
// Only --upload=required makes it count, as exit 4, and even then a failed
// test still exits 1.
//
// NEVER SENDS SOURCE. The parts are the lane's results file (secret-scrubbed
// at capture), its coverage document (repo paths, git blob ids, positions and
// counts) and the diff's changed line ranges.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The upload statuses of testreport.Upload and testUploadRun.
const (
	testUploadOK      = "ok"
	testUploadPartial = "partial"
	testUploadSkipped = "skipped"
	testUploadFailed  = "failed"
)

// testUploadInCI says whether this process runs in CI, where an upload is on
// by default. A seam: tests decide it.
var testUploadInCI = buildinfo.IsCI

// testUploadControlPlane is the control plane test runs are recorded on. A
// seam: tests point it at a fake.
var testUploadControlPlane = projectTestControlPlane

// testUploadWanted decides whether a report is uploaded (§1.6): an explicit
// --upload or --upload=required always asks; otherwise forge.yaml's
// tests.upload decides, `ci` (the default) uploading from CI only and
// `always` from a local clean tree too. `never` refuses even an explicit
// --upload, and says so. An empty why with want false is no upload, silently.
func testUploadWanted(flag, configured string, inCI bool) (want bool, why string) {
	explicit := flag == uploadOn || flag == uploadRequired
	switch {
	case configured == config.TestsUploadNever:
		if explicit {
			return false, "forge.yaml sets tests.upload: never"
		}
		return false, ""
	case flag == uploadOff:
		return false, ""
	case explicit:
		return true, ""
	case configured == config.TestsUploadAlways:
		return true, ""
	}
	return inCI, ""
}

// applyTestUpload uploads rep's runs when the --upload flag and forge.yaml
// say to, records the outcome in rep.Upload with one line on errOut, and
// makes a required upload that did not happen exit 4. A failed test still
// exits 1: the run's own verdict outranks the upload.
func applyTestUpload(ctx context.Context, errOut io.Writer, rep *testreport.Report, flag string, in testUploadInput) error {
	tests, err := testsConfig()
	if err != nil {
		return err
	}
	want, why := testUploadWanted(flag, tests.Upload, testUploadInCI())
	switch {
	case want:
		in.project = hostedProjectName()
		if in.project == "" {
			in.project = rep.Project
		}
		in.flakePolicy = testFlakePolicyWire(tests.Flakes)
		in.goModule = testGoModule(in.root)
		up := uploadTestReport(ctx, in)
		rep.Upload = &up.Upload
		// One warning per omitted part: the run is recorded, but not
		// all of it, and a reader of the control plane cannot tell.
		for _, o := range up.omitted() {
			fmt.Fprintf(errOut, "forge test report: warning: lane %s of run %s was uploaded without its %s: %s\n", o.Lane, o.Run, o.Part, o.Reason)
		}
		fmt.Fprintln(errOut, testUploadNote(up))
	case why != "":
		up := &testUpload{Upload: testreport.Upload{Status: testUploadSkipped, Error: why}}
		rep.Upload = &up.Upload
		fmt.Fprintln(errOut, testUploadNote(up))
	default:
		return nil
	}
	if rep.Upload.Status != testUploadOK && flag == uploadRequired && rep.ExitCode != exitWrong {
		rep.ExitCode = exitRefused
		rep.Error = "upload required, and it did not happen: " + rep.Upload.Error
		if rep.Upload.Status == testUploadPartial {
			rep.Error = "upload required, and it was partial: " + rep.Upload.Error
		}
		rep.OK = false
	}
	return nil
}

// testsConfig is forge.yaml's tests block; zero outside a project.
func testsConfig() (config.TestsConfig, error) {
	cfg, err := loadProjectConfig()
	switch {
	case errors.Is(err, ErrProjectConfigNotFound):
		return config.TestsConfig{}, nil
	case err != nil:
		return config.TestsConfig{}, err
	}
	return cfg.Tests, nil
}

// testGoModule is the module path of root's go.mod, "" when it has none (or
// one too long for RecordTestRun).
func testGoModule(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	mod := modfile.ModulePath(data)
	if len(mod) > maxTestGoModule {
		return ""
	}
	return mod
}

// testUpload is an upload's outcome: the report's `upload` field, and each
// lane run's.
type testUpload struct {
	testreport.Upload
	// Runs are one per lane of each run in the report.
	Runs []testUploadRun
}

// omitted are the parts recorded lane runs were sent without.
func (u *testUpload) omitted() []testreport.UploadOmission {
	if u.Omitted == nil {
		return nil
	}
	return *u.Omitted
}

// testUploadRun is one lane run's upload.
type testUploadRun struct {
	ID, Lane string
	// Status is ok, partial (recorded without a part: Omitted), skipped or
	// failed; Error says why when not ok.
	Status string
	// Omitted are the parts the run was recorded without, with reasons.
	Omitted []testreport.UploadOmission
	// RunID is the control plane's id of the recorded run. Created is
	// false for a retry of results it already held (still ok).
	RunID   string
	Created bool
	URL     string
	Error   string
}

// testUploadInput is what one upload reads.
type testUploadInput struct {
	root    string
	project string
	// flakePolicy and goModule go on every lane run's request when set.
	flakePolicy *wireTestFlakePolicy
	goModule    string
	runs        []*testlocal.Run
	a           *testlocal.Assembly
}

// uploadTestReport records each lane run of in.runs on the control plane and
// says how it went. It never returns an error: every failure is the
// returned Upload's status.
func uploadTestReport(ctx context.Context, in testUploadInput) *testUpload {
	up := &testUpload{}
	each := func(status, why string) {
		for _, r := range in.runs {
			for _, l := range r.Lanes {
				up.Runs = append(up.Runs, testUploadRun{ID: r.ID, Lane: l.Lane, Status: status, Error: why})
			}
		}
	}
	finish := func(status, why string) *testUpload {
		each(status, why)
		up.Status, up.Error = status, why
		return up
	}
	if in.a.Dirty {
		return finish(testUploadSkipped, "the runs are on a dirty tree, and a run on uncommitted changes is never uploaded: commit, then run the lanes again")
	}
	if in.project == "" {
		return finish(testUploadSkipped, "no project name (forge.yaml `name`) to record the runs under")
	}
	decl, err := testUploadControlPlane(ctx)
	if err != nil {
		return finish(testUploadFailed, err.Error())
	}
	ep, err := cloud.ResolveEndpoint("", decl)
	if err != nil {
		return finish(testUploadFailed, err.Error())
	}
	ep.ProjectName = in.project
	if pid := boundProjectID(ep); pid != "" {
		ep.ProjectID = pid
	}
	// tests:write is federatable: in a GitHub Actions job granted
	// `id-token: write`, the job's own OIDC identity is exchanged for it.
	cred, err := cloud.ResolveCredentialFor("", ep, accesstoken.ScopeTestsWrite)
	if why, refused := cloud.CIIdentityRefusal(err); refused {
		return finish(testUploadSkipped, fmt.Sprintf("%s refused this job's GitHub Actions OIDC token: %s (or set %s to a token with tests:write)",
			ep.URL, why, ep.TokenEnv))
	}
	if err != nil {
		return finish(testUploadSkipped, fmt.Sprintf("not signed in to %s (%v); sign in with `%s login`, set %s to a token with tests:write (%s), "+
			"or in GitHub Actions grant the job `permissions: id-token: write`",
			ep.URL, firstLine(err.Error()), Name(), ep.TokenEnv, cloud.TokenSettingsWhere(ep.URL)))
	}
	client := cloud.NewClient(ep, cred)
	client.HTTP.Timeout = testUploadTimeout
	// RecordTestRun takes tests:write (or deploy:write) from a machine
	// credential. A session's credential — a Reliant daemon's, through the
	// credential helper, or a stored `forge login` — is minted for
	// LoginScopes, which hold deploy:write only for a person granted it, so
	// the client elevates for tests:write before the first upload: the person
	// behind the session may hold that narrow scope without deploy:write. An
	// explicit token (FORGE_CONTROL_PLANE_TOKEN, --token) and a CI-identity
	// token (minted for exactly tests:write) are presented as they are, and a
	// refused elevation falls back to the base credential.
	client.DeclareScope(testUploadScope, procRecordTestRun)
	features, err := hostedTestFeatures(ctx, client)
	if err != nil {
		return finish(testUploadFailed, fmt.Sprintf("could not ask %s whether it records test runs: %v", ep.URL, err))
	}
	if !containsString(features, hostedTestFeatureResults) {
		return finish(testUploadSkipped, hostedTestsNotStoredMsg+" ("+ep.URL+")")
	}

	console := deployConsole(ep.URL, in.project)
	diff := testUploadDiff(in.a)
	var ok, partial, failed int
	var firstErr, notServed string
	for _, r := range in.runs {
		for li := range r.Lanes {
			l := &r.Lanes[li]
			ur := testUploadRun{ID: r.ID, Lane: l.Lane}
			if notServed != "" {
				ur.Status, ur.Error = testUploadSkipped, hostedTestsNotStoredMsg
				up.Runs = append(up.Runs, ur)
				continue
			}
			req, omitted, skip, err := testUploadRequest(ctx, in, r, l, diff)
			switch {
			case err != nil:
				ur.Status, ur.Error = testUploadFailed, err.Error()
			case skip != "":
				ur.Status, ur.Error = testUploadSkipped, skip
			default:
				resp, err := recordTestRun(ctx, client, req)
				if hostedUnimplemented(err) {
					// The rpc is not served, whatever the capability
					// said: nothing else will be recorded either.
					notServed = hostedTestsNotStoredMsg + " (" + ep.URL + ")"
					ur.Status, ur.Error = testUploadSkipped, hostedTestsNotStoredMsg
					break
				}
				if err != nil {
					ur.Status, ur.Error = testUploadFailed, firstLine(err.Error())
					break
				}
				ur.Status, ur.RunID, ur.Created, ur.URL = testUploadOK, resp.RunID, resp.Created, resp.URL
				if ur.URL == "" && resp.RunID != "" {
					ur.URL = console.TestRun(resp.RunID)
				}
				if len(omitted) > 0 {
					ur.Status, ur.Omitted = testUploadPartial, omitted
				}
			}
			switch ur.Status {
			case testUploadOK:
				ok++
			case testUploadPartial:
				ok++
				partial++
			case testUploadFailed:
				failed++
				if firstErr == "" {
					firstErr = fmt.Sprintf("lane %s of run %s: %s", ur.Lane, ur.ID, ur.Error)
				}
			}
			up.Runs = append(up.Runs, ur)
		}
	}
	return testUploadVerdict(up, ok, partial, failed, firstErr, notServed, console)
}

// testUploadVerdict sets the upload's status: failed when any lane run
// failed; partial when runs were recorded but one was sent without a part it
// had (each named in Omitted); ok when at least one was recorded whole and
// none was partial; else skipped (with why, or the first lane's reason).
func testUploadVerdict(up *testUpload, ok, partial, failed int, firstErr, why string, console cloud.Console) *testUpload {
	var omitted []testreport.UploadOmission
	for _, r := range up.Runs {
		omitted = append(omitted, r.Omitted...)
	}
	if len(omitted) > 0 {
		up.Omitted = &omitted
	}
	switch {
	case failed > 0:
		up.Status, up.Error = testUploadFailed, firstErr
		if failed > 1 {
			up.Error += fmt.Sprintf(" (and %d more)", failed-1)
		}
	case partial > 0 && why == "":
		o := omitted[0]
		up.Status = testUploadPartial
		up.Error = fmt.Sprintf("lane %s of run %s was recorded without its %s: %s", o.Lane, o.Run, o.Part, o.Reason)
		if n := len(omitted) - 1; n > 0 {
			up.Error += fmt.Sprintf(" (and %d more omitted part(s))", n)
		}
	case ok > 0 && why == "":
		up.Status = testUploadOK
	default:
		up.Status, up.Error = testUploadSkipped, why
		if why == "" && len(up.Runs) > 0 {
			up.Error = up.Runs[0].Error
		}
		if up.Error == "" {
			up.Error = "the report has no lane run to upload"
		}
	}
	if ok == 1 {
		for _, r := range up.Runs {
			if r.Status == testUploadOK || r.Status == testUploadPartial {
				up.URL = r.URL
			}
		}
	} else if ok > 1 {
		up.URL = console.Tests()
	}
	return up
}

// testUploadShaForm is a full lowercase git object id (sha1 or sha256), what
// RecordTestRun takes for a commit, a tree and a base.
var testUploadShaForm = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// testUploadDiff is the report's diff as the part that carries it; nil when
// the report has no diff against a full base sha.
func testUploadDiff(a *testlocal.Assembly) *testreport.Diff {
	if a.Changed == nil || !testUploadShaForm.MatchString(a.BaseSHA) {
		return nil
	}
	return &testreport.Diff{Head: testreport.Head{Schema: testreport.SchemaDiff, OK: true}, Base: a.BaseSHA, Commit: a.Commit, Files: a.Changed}
}

// testUploadAssemble assembles one lane for its upload. A seam: tests make
// it fail.
var testUploadAssemble = testlocal.Assemble

// testUploadRequest is one lane run's RecordTestRun, and the parts it goes
// without: a lane that captured coverage whose document could not be built
// is still recorded (its results are worth having), with the coverage named
// as omitted rather than silently left out. A lane that cannot be recorded
// says why (skip); a lane whose documents could not be built is an error.
func testUploadRequest(ctx context.Context, in testUploadInput, r *testlocal.Run, l *testlocal.Lane, diff *testreport.Diff) (*wireRecordTestRunRequest, []testreport.UploadOmission, string, error) {
	commit := strings.ToLower(r.Commit)
	if !testUploadShaForm.MatchString(commit) {
		return nil, nil, "the run recorded no full commit sha", nil
	}
	tree := strings.ToLower(r.TreeHash)
	if tree == "" {
		// A clean run's tree is its commit's.
		if t, err := testGitLine(ctx, in.root, "rev-parse", "--verify", "--quiet", commit+"^{tree}"); err == nil {
			tree = t
		}
	}
	if !testUploadShaForm.MatchString(tree) {
		return nil, nil, "the run recorded no full tree hash", nil
	}
	req := &wireRecordTestRunRequest{
		Project: in.project, GitCommit: commit, TreeHash: tree, Lane: l.Lane,
		Source: testSourceLocal, ExitCode: l.ExitCode, ForgeVersion: r.ForgeVersion,
		FlakePolicy: in.flakePolicy, GoModule: in.goModule,
	}
	if len(r.Branch) <= 256 {
		req.Branch = r.Branch
	}
	if diff != nil {
		req.BaseSHA = diff.Base
	} else if base := strings.ToLower(r.BaseSHA); testUploadShaForm.MatchString(base) {
		req.BaseSHA = base
	}
	if r.Source() == "ci" {
		req.Source = testSourceCI
		req.CI = &wireTestRunCI{Provider: r.CI.Provider, RunID: r.CI.RunID, Attempt: max(r.CI.Attempt, 0)}
		if u, err := url.Parse(r.CI.RunURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			req.CI.RunURL = r.CI.RunURL
		}
	}
	if req.ForgeVersion == "" {
		req.ForgeVersion = buildinfo.Version()
	}
	if len(req.ForgeVersion) > 128 {
		req.ForgeVersion = req.ForgeVersion[:128]
	}
	if !l.StartedAt.IsZero() {
		t := l.StartedAt.UTC()
		req.StartedAt = &t
	}
	if !l.FinishedAt.IsZero() {
		t := l.FinishedAt.UTC()
		req.FinishedAt = &t
	}

	// The lane alone, for its own coverage (and its results when they are
	// in no format the control plane reads raw).
	la, laErr := testUploadAssemble(ctx, testlocal.Options{ProjectRoot: in.root, Runs: []*testlocal.Run{r}, Lane: l.Lane, NoDiff: true, Git: testGit})
	if laErr != nil {
		la = nil
	}

	results, format, skip, err := testUploadResults(l, la)
	if skip != "" || err != nil {
		return nil, nil, skip, err
	}
	if req.Results, err = gzipTestPart(results, format); err != nil {
		return nil, nil, "", err
	}
	var omitted []testreport.UploadOmission
	if reason := testUploadCoverageOmitted(l, la, laErr); reason != "" {
		omitted = append(omitted, testreport.UploadOmission{Run: r.ID, Lane: testreport.Lane(l.Lane), Part: "coverage", Reason: reason})
	}
	if la != nil && la.Coverage != nil {
		cov := *la.Coverage
		cov.Changed = nil // the diff is its own part
		doc, err := json.Marshal(&cov)
		if err != nil {
			return nil, nil, "", err
		}
		if req.Coverage, err = gzipTestPart(doc, testFormatCoverage); err != nil {
			return nil, nil, "", err
		}
	}
	if diff != nil && req.BaseSHA != "" {
		doc, err := json.Marshal(diff)
		if err != nil {
			return nil, nil, "", err
		}
		if req.Diff, err = gzipTestPart(doc, testFormatDiff); err != nil {
			return nil, nil, "", err
		}
	}
	var sent, inflated int
	for _, p := range req.parts() {
		sent += len(p.Data)
		inflated += p.inflated
	}
	if sent > maxTestUploadSent {
		return nil, nil, "", fmt.Errorf("the lane's documents are %d bytes gzip-compressed; the control plane takes at most %d", sent, maxTestUploadSent)
	}
	if inflated > maxTestUploadInflated {
		return nil, nil, "", fmt.Errorf("the lane's documents are %d bytes uncompressed; the control plane takes at most %d", inflated, maxTestUploadInflated)
	}
	return req, omitted, "", nil
}

// testUploadCoverageOmitted says why a lane that captured coverage has no
// coverage document to send, or "" when it has one (or captured none).
func testUploadCoverageOmitted(l *testlocal.Lane, la *testlocal.Assembly, err error) string {
	if len(l.CoverageInputs()) == 0 {
		return ""
	}
	switch {
	case err != nil:
		return "its coverage could not be assembled: " + firstLine(err.Error())
	case la == nil || la.Coverage == nil:
		why := "its coverage files held nothing forge could read"
		if la != nil && len(la.Notes) > 0 {
			why += " (" + firstLine(la.Notes[0]) + ")"
		}
		return why
	}
	return ""
}

// testUploadResults is the lane's results document and its format: the
// results file as captured (secret-scrubbed) when it is a format the control
// plane ingests raw, else forge's normalised document.
func testUploadResults(l *testlocal.Lane, la *testlocal.Assembly) ([]byte, string, string, error) {
	p := l.ResultsPath()
	if p == "" {
		return nil, "", "the lane captured no results file", nil
	}
	switch l.Format {
	case testreport.LaneFormatGoJSON, testreport.LaneFormatJUnit, testreport.LaneFormatVitest:
		f, err := os.Open(p)
		if err != nil {
			return nil, "", "", err
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(io.LimitReader(f, maxTestUploadInflated+1))
		if err != nil {
			return nil, "", "", err
		}
		if len(b) > maxTestUploadInflated {
			return nil, "", "", fmt.Errorf("the results file is over %d bytes", maxTestUploadInflated)
		}
		if len(b) == 0 {
			return nil, "", "the lane's results file is empty", nil
		}
		return b, string(l.Format), "", nil
	}
	if la == nil || len(la.Results) == 0 || la.Results[0] == nil {
		return nil, "", fmt.Sprintf("the lane's results are in no format forge reads (%s)", orDash(string(l.Format))), nil
	}
	b, err := json.Marshal(la.Results[0])
	return b, testFormatResults, "", err
}

// testGitLine runs one git command through the test verbs' git.
func testGitLine(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := testGit(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// projectTestControlPlane is the control plane this project records test runs
// on: the one its envs declare, else Reliant cloud (as `forge login` and
// `forge release record` default). Test runs are per project, not per env, so
// envs that declare different control planes leave it ambiguous, and that is
// refused rather than guessed.
func projectTestControlPlane(ctx context.Context) (*cloud.Declaration, error) {
	cfgPath, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return &cloud.Declaration{Endpoint: cloud.DefaultEndpoint}, nil
	}
	if err != nil {
		return nil, err
	}
	envs, err := ListEnvs(filepath.Dir(cfgPath))
	if err != nil {
		return nil, err
	}
	var (
		found   []*cloud.Declaration
		seen    = map[string]bool{}
		skipped []string
	)
	for _, env := range envs {
		t, err := resolveObsTarget(ctx, env)
		if err != nil {
			skipped = append(skipped, env)
			continue
		}
		d := t.Declaration()
		if d == nil {
			continue
		}
		key, err := credentials.Normalize(d.Endpoint)
		if err != nil {
			key = d.Endpoint
		}
		if !seen[key] {
			seen[key] = true
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		names := make([]string, len(found))
		for i, d := range found {
			names[i] = d.Endpoint
		}
		return nil, fmt.Errorf("this project's envs declare %d control planes (%s), and test runs are recorded per project on one", len(found), strings.Join(names, ", "))
	case len(skipped) > 0:
		return nil, fmt.Errorf("no env that rendered declares a control plane, and %s did not render: one of those may declare it", strings.Join(skipped, ", "))
	}
	return &cloud.Declaration{Endpoint: cloud.DefaultEndpoint}, nil
}

// testUploadNote is the one stderr line an upload prints.
func testUploadNote(up *testUpload) string {
	if up.Status == testUploadOK {
		n := 0
		for _, r := range up.Runs {
			if r.Status == testUploadOK || r.Status == testUploadPartial {
				n++
			}
		}
		line := fmt.Sprintf("forge test report: uploaded %d lane run(s)", n)
		if up.URL != "" {
			line += ": " + up.URL
		}
		return line
	}
	return fmt.Sprintf("forge test report: upload %s: %s; the report stays in .forge/tests/", up.Status, up.Error)
}
