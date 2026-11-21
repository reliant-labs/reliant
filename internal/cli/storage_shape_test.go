package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderStorageEnv renders an env whose Bundle carries body (KCL attribute
// lines) beside one hosted workload and a control plane, through the seam
// `forge env shape` uses.
func renderStorageEnv(t *testing.T, body string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"storage\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    ` + body + `
    workloads = [fw.Workload {name = "web", image = "ghcr.io/acme/web", runtime = forge.OnHosted {}}]
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// storageShapeOf projects e's declaration fields the way `forge env shape`
// does, around no manifests.
func storageShapeOf(t *testing.T, e *KCLEntities) release.Shape {
	t.Helper()
	s, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:       release.EnvSelfManaged,
		Workloads:  []release.ShapeWorkload{{Name: "web", Runtime: "hosted"}},
		DataAccess: shapeDataAccessOf(e),
		Buckets:    shapeBucketsOf(e),
	})
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	return s
}

// Every declared bucket reaches the recorded shape, whatever its runtime,
// names only; the hosted ones still alone drive provisioning (Buckets).
func TestDeclaredBucketsReachTheShape(t *testing.T) {
	e := renderStorageEnv(t, `buckets = [
        forge.Bucket {name = "uploads", runtime = forge.OnHosted {}, public_read = True}
        forge.Bucket {name = "media", runtime = forge.BucketOnS3 {
            endpoint = "https://acct.r2.cloudflarestorage.com"
            region = "auto"
            access_key_id = forge.ManagedSecret {name = "MEDIA_ACCESS_KEY_ID"}
            secret_access_key = forge.ManagedSecret {name = "MEDIA_SECRET_ACCESS_KEY"}
        }}
    ]`)
	if len(e.Buckets) != 1 || e.Buckets[0].Name != "uploads" {
		t.Errorf("the provisioned buckets changed: %+v", e.Buckets)
	}
	s := storageShapeOf(t, e)
	want := []release.ShapeBucket{
		{Name: "media", RuntimeKind: release.BucketRuntimeS3, Bucket: "media", Endpoint: "https://acct.r2.cloudflarestorage.com"},
		{Name: "uploads", RuntimeKind: release.BucketRuntimeHosted, PublicRead: true},
	}
	if !reflect.DeepEqual(s.Buckets, want) {
		t.Fatalf("shape buckets:\n got %+v\nwant %+v", s.Buckets, want)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "MEDIA_SECRET_ACCESS_KEY") || strings.Contains(string(raw), "MEDIA_ACCESS_KEY_ID") {
		t.Errorf("the shape names a bucket credential: %s", raw)
	}
}

// An env with no bucket records no key: its shape hashes as it did before
// the field, and needs nothing of the reader.
func TestNoBucketsNoShapeKey(t *testing.T) {
	e := renderStorageEnv(t, "")
	if e.DeclaredBuckets != nil {
		t.Errorf("an env with no bucket rendered declared_buckets: %+v", e.DeclaredBuckets)
	}
	s := storageShapeOf(t, e)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"buckets"`) || strings.Contains(string(raw), `"data_access"`) {
		t.Errorf("a bare env records a storage key: %s", raw)
	}
	if s.HasOptionalParts() {
		t.Error("a bare env asks the reader for its features")
	}
}

// data_access.storage reaches the shape only where it differs from db_query,
// and resolves from db_query where it is unset.
func TestDataAccessStorageReachesTheShape(t *testing.T) {
	for _, tc := range []struct {
		decl     string
		recorded *release.ShapeDataAccess
		resolved release.ShapeDataAccess
	}{
		{"", nil, release.ShapeDataAccess{DBQuery: release.DBQueryAudited, Storage: release.StorageAudited}},
		{`data_access = forge.DataAccess {db_query = "deny"}`,
			&release.ShapeDataAccess{DBQuery: release.DBQueryDeny},
			release.ShapeDataAccess{DBQuery: release.DBQueryDeny, Storage: release.StorageDeny}},
		{`data_access = forge.DataAccess {storage = "deny"}`,
			&release.ShapeDataAccess{DBQuery: release.DBQueryAudited, Storage: release.StorageDeny},
			release.ShapeDataAccess{DBQuery: release.DBQueryAudited, Storage: release.StorageDeny}},
		{`data_access = forge.DataAccess {db_query = "open", storage = "open"}`,
			&release.ShapeDataAccess{DBQuery: release.DBQueryOpen},
			release.ShapeDataAccess{DBQuery: release.DBQueryOpen, Storage: release.StorageOpen}},
	} {
		e := renderStorageEnv(t, tc.decl)
		s := storageShapeOf(t, e)
		if !reflect.DeepEqual(s.DataAccess, tc.recorded) {
			t.Errorf("%q: shape records %+v, want %+v", tc.decl, s.DataAccess, tc.recorded)
		}
		if got := s.DataAccess.Resolved(); got != tc.resolved {
			t.Errorf("%q: resolves to %+v, want %+v", tc.decl, got, tc.resolved)
		}
	}
}

// The render's declared_buckets decode straight into the shape, and a reader
// that predates the storage feature is sent neither the buckets nor the
// storage policy. Hermetic: a literal render, no KCL.
func TestStorageShapeFromALiteralRender(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output":{
		"data_access":{"db_query":"audited","storage":"deny"},
		"declared_buckets":[{"name":"uploads","runtime_kind":"hosted"},{"name":"avatars","runtime_kind":"minio","public_read":true}],
		"workloads":[]}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s := storageShapeOf(t, e)
	if len(s.Buckets) != 2 || s.Buckets[0].Name != "avatars" || !s.Buckets[0].PublicRead {
		t.Fatalf("shape buckets: %+v", s.Buckets)
	}
	if s.DataAccess == nil || s.DataAccess.Storage != release.StorageDeny {
		t.Fatalf("shape data access: %+v", s.DataAccess)
	}
	old := (&release.BundleReader{Features: []string{release.BundleFeatureDataAccess}}).LimitShape(s)
	if old.Buckets != nil || old.DataAccess == nil || old.DataAccess.Storage != "" {
		t.Errorf("a reader without the storage feature: buckets %+v, data access %+v", old.Buckets, old.DataAccess)
	}
	// The sealed bundle carries what the declaration recorded.
	in := bundleShapeInputOf(envShapeDoc{Shape: s})
	if !reflect.DeepEqual(in.Buckets, s.Buckets) || !reflect.DeepEqual(in.DataAccess, s.DataAccess) {
		t.Errorf("the bundle input lost the storage declaration: %+v %+v", in.Buckets, in.DataAccess)
	}
}
