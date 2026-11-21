// Clean fixture: a handler test that uses neither the hand-rolled shape
// nor `tdd.RunRPCCases`. The rule must NOT fire — projects that ship
// canonical Go test funcs without a per-RPC table aren't doing anything
// wrong, they just haven't adopted the table-driven shape yet.

package empty_test

import "testing"

func TestSomething(t *testing.T) {
	t.Parallel()
}
