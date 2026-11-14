// Package flux is the pre-1.0 home of what now lives in the public
// forge/pkg/flux. It re-exports it so existing importers keep compiling; new
// code imports forge/pkg/flux directly.
package flux

import pkgflux "github.com/reliant-labs/forge/pkg/flux"

// Flux chart and controller constants mirror their public pkg/flux definitions.
const (
	ChartOCI                = pkgflux.ChartOCI
	ChartVersion            = pkgflux.ChartVersion
	ChartName               = pkgflux.ChartName
	FluxVersion             = pkgflux.FluxVersion
	InstanceName            = pkgflux.InstanceName
	Namespace               = pkgflux.Namespace
	ReconcilerRole          = pkgflux.ReconcilerRole
	SourceRole              = pkgflux.SourceRole
	DeployServiceAccount    = pkgflux.DeployServiceAccount
	ShardLabel              = pkgflux.ShardLabel
	ConnectedShard          = pkgflux.ConnectedShard
	ConnectedController     = pkgflux.ConnectedController
	ConnectedReconcilerRole = pkgflux.ConnectedReconcilerRole
)

// ChartProvidedBy is [pkgflux.ChartProvidedBy].
func ChartProvidedBy(refs ...string) bool { return pkgflux.ChartProvidedBy(refs...) }
