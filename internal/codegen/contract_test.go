package codegen_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	"github.com/reliant-labs/forge/internal/codegen"
)

// Contract tests for the codegen package.
//
// This file is scaffolded once by `forge generate` (and `forge package
// new`). After the first scaffold it is user-owned — forge will not
// overwrite it. Each row in `cases` exercises one method on the
// contract.go-defined Service interface.
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
// Skill: forge skill load contracts
//
// Add a row by appending a tdd.ContractCase. Each row supplies a Call
// closure that invokes one method and returns (any, error); use Want
// for direct equality, Check for custom assertions, or WantErr for the
// error-path.
func TestContract(t *testing.T) {
	t.Parallel()

	// Construct the implementation. Adjust Deps as your contract grows.
	svc := codegen.New(codegen.Deps{})

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
		t.Skip("contract_test.go: add cases to exercise the codegen.Service contract")
	}
	tdd.TableContract(t, svc, cases)
}
