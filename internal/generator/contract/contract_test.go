package contract_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	"github.com/reliant-labs/forge/internal/generator/contract"
)

// Contract tests for the contract package.
//
// The forge generate scaffolder uses the package leaf name to compute
// the import path; for nested packages like internal/generator/contract
// that produces the wrong path, so the import here was hand-corrected
// after the first scaffold. The user owns this file going forward.
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
// Skill: forge skill load contracts
func TestContract(t *testing.T) {
	t.Parallel()

	// Construct the implementation. Adjust Deps as your contract grows.
	svc := contract.New(contract.Deps{})

	cases := []tdd.ContractCase{
		// {
		//     Name: "MethodX returns expected value",
		//     Call: func() (any, error) { return svc.MethodX(context.Background(), "arg") },
		//     Want: "expected",
		// },
	}

	if len(cases) == 0 {
		t.Skip("contract_test.go: add cases to exercise the contract.Service contract")
	}
	tdd.TableContract(t, svc, cases)
}
