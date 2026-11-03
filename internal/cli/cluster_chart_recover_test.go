package cli

import (
	"strings"
	"testing"
)

func TestChartLockRecoveryRequiresExplicitAttestationBeforeIO(t *testing.T) {
	// Omit the explicit context: the command must reject before any kubectl
	// lookup, regardless of what default/prod kubeconfig is on this machine.
	cmd := newChartLockRecoverCmd()
	cmd.SetArgs([]string{"--owner", "p.prod", "--chart", "flux", "--uid", "u", "--resource-version", "1", "--confirm-terminated", "terminated:h"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires explicit context") {
		t.Fatalf("missing fail-closed validation: %v", err)
	}
}
func TestChartLockRecoveryDocumentsNoAutomaticTakeover(t *testing.T) {
	cmd := newChartLockRecoverCmd()
	for _, want := range []string{"never expires", "suspended writer", "no --force", "kubectl children"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("missing recovery warning %q", want)
		}
	}
	if cmd.Flags().Lookup("force") != nil {
		t.Fatal("recovery must not offer force takeover")
	}
}
