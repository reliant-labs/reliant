package bundle

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func externalDatabases() []release.ShapeExternalDatabase {
	return []release.ShapeExternalDatabase{{
		Name: "ledger", Cluster: "prod-ctx", Namespace: "app", Service: "pgbouncer",
		Database: "ledger_prod", Username: "ledger_ro", PasswordSecret: "ledger-ro",
	}}
}

// Both shape builders in this package record the env's external databases:
// the projection from a fresh render, and the sealed-snapshot path that
// rebuilds its input from the recorded declaration. Either one dropping them
// would seal a bundle whose shape disagrees with the declaration.
func TestShapeBuildersKeepExternalDatabases(t *testing.T) {
	want := release.Shape{Kind: release.EnvPersistent, ExternalDatabases: externalDatabases()}.Canonical().ExternalDatabases

	projected, err := ProjectShape(ShapeInput{Kind: release.EnvPersistent, ExternalDatabases: externalDatabases()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected.ExternalDatabases, want) {
		t.Errorf("ProjectShape lost the external databases:\n got %+v\nwant %+v", projected.ExternalDatabases, want)
	}

	d := release.Shape{Kind: release.EnvPersistent, ExternalDatabases: externalDatabases()}
	in, err := snapshotShapeInput(release.Snapshot{Declaration: &d})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.ExternalDatabases, want) {
		t.Errorf("snapshot input lost the external databases:\n got %+v\nwant %+v", in.ExternalDatabases, want)
	}
}

// Both shape builders carry the project's declared changes, for the same
// reason: a sealed bundle whose shape lacks them disagrees with the
// declaration it was sealed from.
func TestShapeBuildersKeepChanges(t *testing.T) {
	changes := map[string]release.ShapeChange{"new-checkout": {
		Type: release.ChangeTypeBool, Default: "control",
		Variants: map[string]json.RawMessage{"control": json.RawMessage(`false`), "treatment": json.RawMessage(`true`)},
	}}
	projected, err := ProjectShape(ShapeInput{Kind: release.EnvPersistent, Changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected.Changes, changes) {
		t.Errorf("ProjectShape lost the changes:\n got %+v\nwant %+v", projected.Changes, changes)
	}
	d := release.Shape{Kind: release.EnvPersistent, Changes: changes}
	in, err := snapshotShapeInput(release.Snapshot{Declaration: &d})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Changes, changes) {
		t.Errorf("snapshot input lost the changes:\n got %+v\nwant %+v", in.Changes, changes)
	}
}
