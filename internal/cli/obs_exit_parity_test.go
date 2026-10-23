package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// errCaller fails every control-plane call with err.
type errCaller struct{ err error }

func (c errCaller) Call(context.Context, string, any, any) error { return c.err }

// obsParityVerbs is every `forge obs` verb that prints a --json document, with
// the arguments it needs to get past argument validation.
var obsParityVerbs = []struct {
	name string
	args []string
}{
	{"check", []string{"check", "prod"}},
	{"changes", []string{"changes", "prod"}},
	{"status", []string{"status", "prod"}},
	{"logs", []string{"logs", "prod"}},
	{"traces", []string{"traces", "prod"}},
	{"trace", []string{"trace", "0af7651916cd43dd8448eb211c80319c", "prod"}},
	{"endpoints", []string{"endpoints", "prod"}},
	{"query", []string{"query", "prod", "--sql", "select 1"}},
	{"issues", []string{"issues", "prod"}},
	{"issue", []string{"issue", "ISS-1", "prod"}},
	{"metrics list", []string{"metrics", "list", "prod"}},
	{"metrics query", []string{"metrics", "query", "prod", "rpc.server.duration"}},
}

// TestObsJSONDocumentExitCodeIsTheProcessExitCode is the contract agents branch
// on: for every failure class, the process exits with exactly the document's
// exit_code. The process status is exitcode.Of(err) — cmd/forge/main.go's only
// mapping — so the test compares the document against it for each verb.
func TestObsJSONDocumentExitCodeIsTheProcessExitCode(t *testing.T) {
	cloudWith := func(c obsbackend.Caller) func(context.Context, string) (obsbackend.Cloud, error) {
		return func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: c}, nil
		}
	}
	facts := func(f obsbackend.Facts) func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
		return func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return f, nil }
	}
	factsErr := func(err error) func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
		return func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return obsbackend.Facts{}, err }
	}

	classes := []struct {
		name  string
		deps  obsDeps
		extra []string
		want  int // -1: only the parity is asserted
	}{
		{"env declaration cannot be rendered (setup)", obsDeps{Facts: factsErr(obsbackend.Undetermined("cannot read env %q's declaration", "prod"))}, nil, 2},
		{"an unclassified setup error", obsDeps{Facts: factsErr(errors.New("boom"))}, nil, 1},
		{"telemetry is off", obsDeps{Facts: facts(obsbackend.Facts{Env: "prod"})}, nil, 2},
		{"--backend is not a backend", obsDeps{Facts: facts(cloudFacts())}, []string{"--backend", "nope"}, 1},
		{"--backend cloud with no control plane", obsDeps{Facts: facts(obsbackend.Facts{Env: "prod", Declared: true, Enabled: true})}, []string{"--backend", "cloud"}, 2},
		{"auth refused opening the cloud backend", obsDeps{Facts: facts(cloudFacts()), Cloud: func(context.Context, string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{}, obsbackend.Undetermined("env prod: cannot reach its control plane")
		}}, nil, 2},
		{"an unclassified error opening the cloud backend", obsDeps{Facts: facts(cloudFacts()), Cloud: func(context.Context, string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{}, errors.New("boom")
		}}, nil, 1},
		{"control plane unreachable", obsDeps{Facts: facts(cloudFacts()), Cloud: cloudWith(errCaller{io.ErrUnexpectedEOF})}, nil, -1},
		{"control plane refuses the request", obsDeps{Facts: facts(cloudFacts()), Cloud: cloudWith(errCaller{&cloud.Error{Code: cloud.CodeInvalidArgument}})}, nil, -1},
		{"--fields names no field", obsDeps{Facts: facts(cloudFacts()), Cloud: cloudWith(errCaller{io.ErrUnexpectedEOF})}, []string{"--fields", "not_a_field"}, 1},
	}

	for _, v := range obsParityVerbs {
		for _, c := range classes {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				d := c.deps
				d.Now = func() time.Time { return obsNow }
				var out, errb bytes.Buffer
				old := obsDepsOverride
				t.Cleanup(func() { obsDepsOverride = old })
				obsDepsOverride = &d
				cmd := newTestRootCmd(t)
				cmd.SetOut(&out)
				cmd.SetErr(&errb)
				cmd.SetArgs(append(append([]string{"obs"}, v.args...), append([]string{"--json"}, c.extra...)...))
				err := cmd.ExecuteContext(context.Background())

				process := exitcode.Of(err)
				doc := decode(t, out.String())
				if doc["exit_code"] != float64(process) {
					t.Fatalf("document exit_code = %v, process exits %d (%v)\n%s", doc["exit_code"], process, err, out.String())
				}
				if doc["ok"] != (process == 0) {
					t.Errorf("ok = %v with exit %d", doc["ok"], process)
				}
				if process == 0 {
					t.Fatalf("a failure class exited 0: %s", out.String())
				}
				if c.want >= 0 && process != c.want {
					t.Errorf("exit = %d, want %d (%v)", process, c.want, err)
				}
			})
		}
	}
}
