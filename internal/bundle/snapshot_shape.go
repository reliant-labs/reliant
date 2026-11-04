package bundle

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

// snapshotBuildInput replaces current render inputs with frozen snapshot data.
func snapshotBuildInput(in BuildInput) (BuildInput, error) {
	if in.Snapshot == nil {
		return in, nil
	}
	if err := in.Snapshot.Validate(); err != nil {
		return in, err
	}
	if in.Snapshot.Project != in.Project || in.Snapshot.Environment != in.Env {
		return in, fmt.Errorf("%w: snapshot bundle scope mismatch", release.ErrInvalid)
	}
	shape, err := snapshotShapeInput(*in.Snapshot)
	if err != nil {
		return in, err
	}
	in.Shape = shape
	in.Units = in.Snapshot.DeliveryUnits
	in.Shape.SyncedSecrets = in.Snapshot.SecretRefs
	in.Observability = nil
	for _, asset := range in.Snapshot.Assets {
		in.Observability = append(in.Observability, ObservabilityFile{Path: asset.Path, Data: asset.Data})
	}
	return in, nil
}

func snapshotShapeInput(s release.Snapshot) (ShapeInput, error) {
	if s.Declaration == nil {
		return ShapeInput{}, fmt.Errorf("%w: sealed snapshot requires recorded shape declaration", release.ErrInvalid)
	}
	d := s.Declaration.Canonical()
	return ShapeInput{Kind: d.Kind, Workloads: d.Workloads, Secrets: d.Secrets, Domains: d.Domains, Clusters: d.Clusters, Manifests: SnapshotStream(s), SyncedSecrets: s.SecretRefs, StatefulWorkloads: s.StatefulWorkloads, ControlPlaneSecrets: d.ControlPlaneSecrets, Alerts: d.Alerts, AlertRoutes: d.AlertRoutes, ObservabilityPolicy: d.ObservabilityPolicy, FrontendTelemetry: d.FrontendTelemetry, DeliveryPolicy: d.DeliveryPolicy, DataAccess: d.DataAccess, ReleaseHealth: d.ReleaseHealth, UptimeChecks: d.UptimeChecks, Identity: d.Identity, Databases: d.Databases, Applier: d.Applier, Units: d.Units, Store: release.StoreProjection{StoreSecrets: d.StoreSecrets, GeneratedSecrets: d.GeneratedSecrets, OpenBao: d.OpenBao, ExternalSecretsOperators: d.ExternalSecretsOperators}}, nil
}
