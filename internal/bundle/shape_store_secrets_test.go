package bundle

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func storeProjection() release.StoreProjection {
	return release.StoreProjection{
		StoreSecrets: []release.ShapeStoreSecret{{
			Cluster: "gke-prod", ConnectedCluster: "prod", Namespace: "shop-prod", Store: release.StoreHosted,
			Names: []string{"STRIPE_SECRET_KEY"}, Pins: []release.ShapeStorePin{{Name: "STRIPE_SECRET_KEY", Version: 2}},
		}},
		GeneratedSecrets:         []release.ShapeGeneratedSecret{{Store: release.StoreHosted, Kind: "random", Names: []string{"STRIPE_SECRET_KEY"}}},
		ExternalSecretsOperators: []release.ShapeExternalSecretsOperator{{Cluster: "gke-prod", ConnectedCluster: "prod", Namespace: "external-secrets", Version: "2.13.0"}},
	}
}

// The one projection records the store-secret fields, in canonical form, and a
// declaration without them records none.
func TestProjectShapeRecordsStoreSecrets(t *testing.T) {
	in := fixtureInput()
	bare := projectFixture(t, in)
	if bare.StoreSecrets != nil || bare.GeneratedSecrets != nil || bare.OpenBao != nil || bare.ExternalSecretsOperators != nil {
		t.Fatalf("a declaration with no store secrets records some: %+v", bare)
	}

	in.Store = storeProjection()
	got := projectFixture(t, in)
	want := storeProjection()
	if !reflect.DeepEqual(got.StoreSecrets, want.StoreSecrets) || !reflect.DeepEqual(got.ExternalSecretsOperators, want.ExternalSecretsOperators) {
		t.Errorf("store fields not recorded:\n got %+v\nwant %+v", got, want)
	}
	if len(got.GeneratedSecrets) != 1 || got.GeneratedSecrets[0].Bytes != 32 || got.GeneratedSecrets[0].Encoding != "base64" {
		t.Errorf("generated secret not canonical: %+v", got.GeneratedSecrets)
	}

	a, _ := bare.Encode()
	b, _ := got.Encode()
	if string(a) == string(b) {
		t.Error("recording store secrets did not change the shape's encoding")
	}
}

// The sealed-snapshot path rebuilds its ShapeInput from the recorded
// declaration; the store fields must come back, or a sealed bundle's shape
// would disagree with the declaration it was sealed from.
func TestSnapshotShapeInputKeepsStoreSecrets(t *testing.T) {
	d := release.Shape{Kind: release.EnvPersistent}.WithStoreSecrets(storeProjection())
	got, err := snapshotShapeInput(release.Snapshot{Declaration: &d})
	if err != nil {
		t.Fatal(err)
	}
	canon := d.Canonical()
	want := release.StoreProjection{StoreSecrets: canon.StoreSecrets, GeneratedSecrets: canon.GeneratedSecrets, OpenBao: canon.OpenBao, ExternalSecretsOperators: canon.ExternalSecretsOperators}
	if !reflect.DeepEqual(got.Store, want) {
		t.Errorf("snapshot input lost the store fields:\n got %+v\nwant %+v", got.Store, want)
	}
}
