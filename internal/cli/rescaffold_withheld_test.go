// Rescaffold of a per-RPC scaffold test generate would not write, and of a
// path whose re-creation fails: the birth record survives either way.
//
// The Ranger repro: the scaffold test of an implemented RPC, renamed to
// rpc_get_job_status_test.go, then `forge project rescaffold
// .../handlers_scaffold_get_job_status_test.go`. A full generate ran and wrote
// nothing (an RPC's scaffold test is born only beside its fresh stub), the
// command failed "forge does not scaffold ... check the path", and the ledger
// entry it had dropped stayed dropped — so the deleted scaffold read as never
// scaffolded.
//
// Not t.Parallel: the birth ledger's in-memory cache is process-global.
package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

const withheldScaffoldTest = "internal/handlers/jobs/handlers_scaffold_get_job_status_test.go"

// jobsHandlerProject is a project whose jobs handler declares GetJobStatus
// (body is the method's body) and whose scaffold test for it was born and
// then deleted.
func jobsHandlerProject(t *testing.T, body string) string {
	t.Helper()
	root := newLedgerProject(t)
	dir := filepath.Join(root, "internal", "handlers", "jobs")
	mustWriteFile(t, filepath.Join(dir, "service.go"), "package jobs\n\ntype Service struct{}\n")
	if body != "" {
		mustWriteFile(t, filepath.Join(dir, "rpc_get_job_status.go"), "package jobs\n\nimport \"context\"\n\n"+
			"func (s *Service) GetJobStatus(ctx context.Context, req any) (any, error) {\n"+body+"\n}\n")
	}
	checksums.RecordScaffold(root, withheldScaffoldTest)
	return root
}

func rescaffoldWithheld(t *testing.T, root string) (pipelineRan bool, err error) {
	t.Helper()
	var w bytes.Buffer
	err = rescaffoldPaths(&w, root, loadAdvisoryConfig(t, root), []string{withheldScaffoldTest}, rescaffoldGenerate{
		pipeline: func() error { pipelineRan = true; return nil },
	})
	return pipelineRan, err
}

func TestRescaffold_ScaffoldTestOfAnImplementedRPCSaysWhyAndKeepsTheRecord(t *testing.T) {
	t.Run("renamed", func(t *testing.T) {
		root := jobsHandlerProject(t, "\treturn req, nil")
		mustWriteFile(t, filepath.Join(root, "internal/handlers/jobs/rpc_get_job_status_test.go"),
			"package jobs_test\n\nimport \"testing\"\n\nfunc TestGetJobStatus_Generated(t *testing.T) {}\n")
		pipelineRan, err := rescaffoldWithheld(t, root)
		if err == nil {
			t.Fatal("rescaffold of the scaffold test of an implemented RPC succeeded")
		}
		if pipelineRan {
			t.Error("rescaffold ran a whole generate for a file it already knew no renderer writes")
		}
		for _, want := range []string{"GetJobStatus is implemented (internal/handlers/jobs/rpc_get_job_status.go)",
			"internal/handlers/jobs/rpc_get_job_status_test.go already declares TestGetJobStatus_Generated", "nothing is missing"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should say %q, got: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "check the path") {
			t.Errorf("the refusal blames the path instead of saying why: %v", err)
		}
		if !checksums.ScaffoldRecorded(root, withheldScaffoldTest) {
			t.Error("the refused rescaffold dropped the scaffold's birth record: the next generate reads the deletion as never scaffolded")
		}
	})
	t.Run("not renamed", func(t *testing.T) {
		root := jobsHandlerProject(t, "\treturn req, nil")
		pipelineRan, err := rescaffoldWithheld(t, root)
		if err == nil || pipelineRan {
			t.Fatalf("want a refusal before any generate, got err=%v pipelineRan=%v", err, pipelineRan)
		}
		for _, want := range []string{"GetJobStatus is implemented", "CodeUnimplemented", "internal/handlers/jobs/rpc_get_job_status_test.go is the conventional name"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should say %q, got: %v", want, err)
			}
		}
		if !checksums.ScaffoldRecorded(root, withheldScaffoldTest) {
			t.Error("the refused rescaffold dropped the scaffold's birth record")
		}
	})
}

// An RPC still on forge's stub gets its scaffold test only with a fresh
// stub, so the refusal names the stub to delete — and keeps the record.
func TestRescaffold_ScaffoldTestOfAStubbedRPCNamesTheStub(t *testing.T) {
	root := jobsHandlerProject(t, "")
	// The staged `project new` render, which runs first, needs a module.
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.24\n")
	mustWriteFile(t, filepath.Join(root, "forge.yaml"), "name: ledgertest\nmodule_path: example.com/demo\n")
	stub := filepath.Join(root, "internal/handlers/jobs/rpc_get_job_status.go")
	mustWriteFile(t, stub, "package jobs\n\nimport (\n\t\"context\"\n\n\t\"github.com/reliant-labs/forge/pkg/svcerr\"\n)\n\n"+
		"// GetJobStatus implements the GetJobStatus RPC.\n// forge:gen unwired-stub symbol=jobs.GetJobStatus\n"+
		"func (s *Service) GetJobStatus(ctx context.Context, req any) (any, error) {\n"+
		"\treturn nil, svcerr.Wrap(svcerr.ScaffoldStub(\"GetJobStatus\"))\n}\n")
	_, err := rescaffoldWithheld(t, root)
	if err == nil {
		t.Fatal("rescaffold of the scaffold test of a stubbed RPC succeeded without writing it")
	}
	for _, want := range []string{"internal/handlers/jobs/rpc_get_job_status.go still holds forge's Unimplemented stub of GetJobStatus",
		"delete the GetJobStatus stub"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got: %v", want, err)
		}
	}
	if !checksums.ScaffoldRecorded(root, withheldScaffoldTest) {
		t.Error("the failed rescaffold dropped the scaffold's birth record")
	}
}

// A rescaffold that drops a record and then fails puts the record back: the
// path is still absent, and absent-and-unrecorded means "never scaffolded".
func TestRescaffold_FailedReCreationRestoresTheBirthRecord(t *testing.T) {
	root := jobsHandlerProject(t, "") // GetJobStatus not declared: generate would re-stub it
	checksums.DeclineScaffold(root, withheldScaffoldTest, "we test jobs end to end")
	var w bytes.Buffer
	boom := errors.New("buf generate failed")
	err := rescaffoldPaths(&w, root, loadAdvisoryConfig(t, root), []string{withheldScaffoldTest}, rescaffoldGenerate{
		pipeline: func() error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want the generate failure, got: %v", err)
	}
	if reason, ok := checksums.ScaffoldDeclined(root, withheldScaffoldTest); !ok || reason != "we test jobs end to end" {
		t.Errorf("the failed rescaffold left the record as declined=%v reason=%q; want the entry it dropped, decline and all", ok, reason)
	}
}
