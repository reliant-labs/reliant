package flux

import (
	"testing"

	pkgflux "github.com/reliant-labs/forge/pkg/flux"
)

// The alias must never fork from the public package: a second spelling here is
// exactly the drift forge/pkg/flux exists to remove.
func TestAliasesAreThePublicPackage(t *testing.T) {
	t.Parallel()
	for name, pair := range map[string][2]string{
		"ChartOCI":                {ChartOCI, pkgflux.ChartOCI},
		"ChartVersion":            {ChartVersion, pkgflux.ChartVersion},
		"ChartName":               {ChartName, pkgflux.ChartName},
		"Namespace":               {Namespace, pkgflux.Namespace},
		"ReconcilerRole":          {ReconcilerRole, pkgflux.ReconcilerRole},
		"SourceRole":              {SourceRole, pkgflux.SourceRole},
		"DeployServiceAccount":    {DeployServiceAccount, pkgflux.DeployServiceAccount},
		"ShardLabel":              {ShardLabel, pkgflux.ShardLabel},
		"ConnectedShard":          {ConnectedShard, pkgflux.ConnectedShard},
		"ConnectedController":     {ConnectedController, pkgflux.ConnectedController},
		"ConnectedReconcilerRole": {ConnectedReconcilerRole, pkgflux.ConnectedReconcilerRole},
	} {
		if pair[0] == "" || pair[0] != pair[1] {
			t.Errorf("%s: internal/flux = %q, forge/pkg/flux = %q", name, pair[0], pair[1])
		}
	}
	if got, want := ChartProvidedBy(pkgflux.ChartOCI), true; got != want {
		t.Errorf("ChartProvidedBy(ChartOCI) = %v", got)
	}
}
