// The proto-vs-migration "schema drift" check is GONE, and this pins its
// absence so it is not reintroduced by someone reading the old comments.
//
// It compared proto message fields against migration columns BY NAME. That
// pairing was coherent only while proto entity annotations declared the
// schema. Migrations are now the only schema truth and proto is the wire
// shape, so the two are not required to agree, and the check reported every
// legitimate divergence as a defect:
//
//   - users.onboarding_completed is DERIVED — the response message computes
//     it, no column backs it, and none should;
//   - plans.price_cents is stored inside a JSONB column on purpose;
//   - every API response message with no table at all.
//
// On a clean control-plane tree (exit 0, zero files changed) it printed 19
// suggested ALTER statements, none of which may be applied. A suggestion
// that must be ignored every time trains the reader to ignore the whole
// notice block, which is the cost this removal is paying back.
//
// The delete is total: internal/codegen/schemadrift, the always-on notice in
// stepPostGenValidate, and `forge db check` (schemadrift was its only
// engine). There is deliberately no opt-in marker to bring it back — an
// opt-in check with no correct signal is still no correct signal.
package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestNoSchemaDriftCommand asserts `forge db check` is not registered.
// Before the removal this found the subcommand and failed.
func TestNoSchemaDriftCommand(t *testing.T) {
	for _, c := range newDBCmd().Commands() {
		if c.Name() == "check" {
			t.Fatalf("`forge db check` is still registered — the proto-vs-migration drift check was deleted, "+
				"and schemadrift was its only engine (got %q)", c.Short)
		}
	}
}

// TestNoSchemaDriftInCommandHelp asserts no command advertises the retired
// check. A stale Short/Long is how a deleted feature gets reintroduced: the
// next reader sees it documented and assumes the code was lost, not removed.
func TestNoSchemaDriftInCommandHelp(t *testing.T) {
	var offenders []string
	var walk func(*cobra.Command, string)
	walk = func(c *cobra.Command, path string) {
		full := strings.TrimSpace(path + " " + c.Name())
		if strings.Contains(strings.ToLower(c.Short+" "+c.Long), "schema drift") {
			offenders = append(offenders, full)
		}
		for _, sub := range c.Commands() {
			walk(sub, full)
		}
	}
	walk(newTestRootCmd(t), "")
	if len(offenders) > 0 {
		t.Fatalf("these commands still advertise \"schema drift\", which forge no longer checks: %s",
			strings.Join(offenders, ", "))
	}
}
