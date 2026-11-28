// yours: scaffolded once, never touched again — forge will not overwrite this file
// Contract test rows; canonical pattern in `forge skill load testing/patterns`.
package messagewrite_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	"github.com/reliant-labs/reliant/internal/messagewrite"
)

// Contract tests for the messagewrite package.
//
// This file is scaffolded once by `forge generate` (and `forge package
// new`). After the first scaffold it is user-owned — forge will not
// overwrite it. Each row in `cases` exercises one method on the
// package's Service interface (hand-written in contract.go, or derived
// into contract_gen.go from the //forge:contract type).
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
// Skill: forge skill load contracts
//
// Add a row by appending a tdd.ContractCase. Each row supplies a Call
// closure that invokes one method and returns (any, error); use Want
// for direct equality, Check for custom assertions, or WantErr for the
// error-path.
//
// Fill Deps with the MOCKS forge already generated — never a
// hand-rolled fake. `forge generate` writes mock_gen.go next to this
// file, holding one mock for Service, one per interface in deps.go (or
// contract.go), AND one per interface your Deps names from another
// package — so a generated
// `db.EstimateStore` dep has a `messagewrite.MockEstimateStore`, and a
// `Store` dep has a `messagewrite.MockStore`, sitting right there. Set its
// `GetFunc` (etc.) to script the case; every call is recorded by the
// embedded contractkit.Recorder, so `m.Calls("Get")` and
// `m.CallCount("Get")` assert ordering and short-circuiting without
// any bookkeeping of your own.
func TestContract(t *testing.T) {
	t.Parallel()

	// svc is DECLARED here and CONSTRUCTED below the empty-table skip, so a
	// package that has not written its first case yet never calls New. That
	// ordering is what lets you take service.go's advice — grow New into dep
	// validation or eager initialization — without this born test going red
	// on an empty `Deps{}` it was never asked to fill. The Call closures
	// below capture the VARIABLE, so they see the constructed service.
	var svc messagewrite.Service

	cases := []tdd.ContractCase{
		// {
		//     Name: "MethodX returns expected value",
		//     Call: func() (any, error) { return svc.MethodX(context.Background(), "arg") },
		//     Want: "expected",
		// },
		// {
		//     Name:    "MethodY surfaces upstream error",
		//     Call:    func() (any, error) { return nil, svc.MethodY(context.Background()) },
		//     WantErr: someSentinel,
		// },
	}

	if len(cases) == 0 {
		t.Skip("contract_test.go: add cases to exercise the messagewrite.Service contract")
	}

	// Fill Deps with the mocks your cases need. The two-result
	// `(Service, error)` shape matches the canonical service.go scaffold, so
	// a dep-validation or eager-init failure is reported right here instead
	// of surfacing as a nil dereference inside a case.
	svc, err := messagewrite.New(messagewrite.Deps{
		Tx: &messagewrite.MockTx{}, Order: &messagewrite.MockOrder{}, Messages: &messagewrite.MockMessages{},
		Windows: &messagewrite.MockWindows{}, Threads: &messagewrite.MockThreads{}, Workflows: &messagewrite.MockWorkflows{},
		Blocks: &messagewrite.MockBlocks{}, Attachments: &messagewrite.MockAttachments{}, Updates: &messagewrite.MockUpdates{},
	})
	if err != nil {
		t.Fatalf("messagewrite.New: %v", err)
	}

	tdd.TableContract(t, svc, cases)
}
