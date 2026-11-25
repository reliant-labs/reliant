package cli

// The control plane's TestsService, as forge speaks it: the capability that
// says whether this control plane records test runs, and RecordTestRun, which
// `forge test report --upload` calls once per lane run (design doc
// docs/proposals/tests-coverage-flakes.md §1.6, §2.3).
//
// Like every hosted call (hosted_vuln.go, hosted_ledger.go), the request and
// response shapes are declared HERE in proto3 JSON, from control-plane's
// proto/services/tests/v1/tests.proto (control-plane#3286), and carried by
// cloud.Client.Call. forge does not vendor control-plane's protos
// (internal/cloud/client.go says why); a field forge does not read is a field
// forge does not break on.

import (
	"bytes"
	"compress/gzip"
	"context"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

const (
	procRecordTestRun = "controlplane.v1.TestsService/RecordTestRun"

	// hostedTestFeatureResults is GetCapabilities.test_features'
	// "test-results": RecordTestRun stores an uploaded run and ingests it.
	// It gates the upload.
	hostedTestFeatureResults = "test-results"

	testPayloadGzip = "TEST_PAYLOAD_ENCODING_GZIP"
	testSourceCI    = "TEST_RUN_SOURCE_CI"
	testSourceLocal = "TEST_RUN_SOURCE_LOCAL"

	// The formats of forge's normalised documents (TestUploadPart.format).
	testFormatResults  = "forge.test.results/v1"
	testFormatCoverage = "forge.test.coverage/v1"
	testFormatDiff     = "forge.test.diff/v1"

	hostedTestsNotStoredMsg = "this control plane does not store test runs yet"
)

// The upload limits, as RecordTestRun enforces them, checked here first so a
// refusal names its reason before anything is sent. Nothing is truncated.
const (
	// maxTestUploadSent bounds the parts' bytes as sent, gzip-compressed.
	// The control plane takes 32 MiB of parts, but its transport reads at
	// most 33 MiB of request body, and the JSON binding carries bytes as
	// base64 (4/3 larger): 24 MiB of parts is what fits.
	maxTestUploadSent = 24 << 20
	// maxTestUploadInflated bounds the parts' decoded bytes together.
	maxTestUploadInflated = 256 << 20
	// testUploadTimeout bounds one RecordTestRun: a 24 MiB body on a slow
	// CI link takes longer than the client's 30-second default.
	testUploadTimeout = 5 * time.Minute
)

// hostedTestFeatures reads GetCapabilities.test_features. A control plane that
// does not serve GetCapabilities predates every feature; one that serves it
// without the field names none. Any other failure is returned: not knowing is
// not "none".
func hostedTestFeatures(ctx context.Context, client cloudCaller) ([]string, error) {
	var caps struct {
		Features []string `json:"testFeatures"`
	}
	if err := client.Call(ctx, procDeployCapabilities, map[string]any{}, &caps); err != nil {
		if hostedUnimplemented(err) {
			return nil, nil
		}
		return nil, err
	}
	return caps.Features, nil
}

// wireTestUploadPart is TestUploadPart.
type wireTestUploadPart struct {
	// Data is carried as base64, which is proto3 JSON's bytes.
	Data     []byte `json:"data"`
	Encoding string `json:"encoding"`
	Format   string `json:"format"`

	// inflated is the document's size before compression (not sent).
	inflated int
}

// gzipTestPart is a document as the part that carries it: always
// gzip-encoded, which RecordTestRun requires of a part over 1 MiB and which
// costs nothing worth saving below it.
func gzipTestPart(doc []byte, format string) (*wireTestUploadPart, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(doc); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &wireTestUploadPart{Data: buf.Bytes(), Encoding: testPayloadGzip, Format: format, inflated: len(doc)}, nil
}

// wireTestRunCI is TestRunCI.
type wireTestRunCI struct {
	Provider string `json:"provider,omitempty"`
	RunID    string `json:"runId,omitempty"`
	RunURL   string `json:"runUrl,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
}

// wireRecordTestRunRequest is RecordTestRunRequest. environment_id is never
// sent: a run is recorded against an environment only by a lane that tested
// one (`forge test collect`), which the run manifest does not name yet.
type wireRecordTestRunRequest struct {
	Project      string              `json:"project"`
	GitCommit    string              `json:"gitCommit"`
	TreeHash     string              `json:"treeHash"`
	Dirty        bool                `json:"dirty,omitempty"`
	Branch       string              `json:"branch,omitempty"`
	BaseSHA      string              `json:"baseSha,omitempty"`
	Lane         string              `json:"lane"`
	Source       string              `json:"source"`
	CI           *wireTestRunCI      `json:"ci,omitempty"`
	StartedAt    *time.Time          `json:"startedAt,omitempty"`
	FinishedAt   *time.Time          `json:"finishedAt,omitempty"`
	ExitCode     int                 `json:"exitCode"`
	ForgeVersion string              `json:"forgeVersion,omitempty"`
	Results      *wireTestUploadPart `json:"results"`
	Coverage     *wireTestUploadPart `json:"coverage,omitempty"`
	Diff         *wireTestUploadPart `json:"diff,omitempty"`
	// FlakePolicy (field 19, control-plane#3434) is forge.yaml's
	// tests.flakes, which the daily flake sweep acts on; omitted when the
	// project sets none, which the control plane reads as the default (no
	// issue, no pull request). GoModule (field 20) is the root go.mod's
	// module path, which a flake issue's title and a quarantine entry name
	// packages relative to. A control plane that predates either ignores it:
	// Connect's JSON codec discards unknown fields.
	FlakePolicy *wireTestFlakePolicy `json:"flakePolicy,omitempty"`
	GoModule    string               `json:"goModule,omitempty"`
}

// wireTestFlakePolicy is TestFlakePolicy.
type wireTestFlakePolicy struct {
	Issues     bool   `json:"issues,omitempty"`
	Quarantine string `json:"quarantine,omitempty"`
}

// The TestQuarantineMode values.
const (
	testQuarantineSuggest = "TEST_QUARANTINE_MODE_SUGGEST"
	testQuarantinePR      = "TEST_QUARANTINE_MODE_PR"
)

// testFlakePolicyWire is forge.yaml's tests.flakes as TestFlakePolicy; nil
// when it sets nothing.
func testFlakePolicyWire(f config.TestsFlakesConfig) *wireTestFlakePolicy {
	p := &wireTestFlakePolicy{Issues: f.Issues}
	switch f.Quarantine {
	case config.TestsQuarantinePR:
		p.Quarantine = testQuarantinePR
	case config.TestsQuarantineSuggest:
		p.Quarantine = testQuarantineSuggest
	}
	if !p.Issues && p.Quarantine == "" {
		return nil
	}
	return p
}

// maxTestGoModule is RecordTestRunRequest.go_module's limit.
const maxTestGoModule = 512

// parts are the request's documents, as sent.
func (r *wireRecordTestRunRequest) parts() []*wireTestUploadPart {
	var out []*wireTestUploadPart
	for _, p := range []*wireTestUploadPart{r.Results, r.Coverage, r.Diff} {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

// wireRecordTestRunResponse is RecordTestRunResponse.
type wireRecordTestRunResponse struct {
	RunID string `json:"runId"`
	// Created is false for a retry of results the project already holds:
	// the call still succeeds and names that run.
	Created     bool   `json:"created"`
	IngestState string `json:"ingestState"`
	URL         string `json:"url"`
}

// recordTestRun is one RecordTestRun.
func recordTestRun(ctx context.Context, client cloudCaller, req *wireRecordTestRunRequest) (wireRecordTestRunResponse, error) {
	var resp wireRecordTestRunResponse
	err := client.Call(ctx, procRecordTestRun, req, &resp)
	return resp, err
}
