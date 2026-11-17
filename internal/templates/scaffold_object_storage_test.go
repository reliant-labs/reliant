package templates_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// TestScaffoldedDevObjectStorage turns on the scaffolded dev env's object
// storage the way its comment says to (two lines), renders it the way forge
// does, and checks what `forge env up` would act on: a compose workload for
// docker-compose.yml's `minio` told which bucket to create, and the bucket
// itself on the env. Off, the scaffold declares neither, so a project that
// stores no files runs no MinIO.
func TestScaffoldedDevObjectStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a project and renders its dev env twice; runs in task test")
	}
	tmp := scaffoldServiceForRender(t)
	mainPath := filepath.Join(tmp, "deploy/kcl/dev/main.k")
	mainK, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	render := func() map[string]any {
		t.Helper()
		out, err := kclrender.Run(tmp, filepath.Join(tmp, "deploy/kcl/dev"), []string{"env=dev"})
		if err != nil {
			t.Fatalf("render dev env: %v", err)
		}
		var doc struct {
			Output map[string]any `json:"output"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return doc.Output
	}
	minio := func(out map[string]any) map[string]any {
		for _, w := range out["workloads"].([]any) {
			if w := w.(map[string]any); w["name"] == "minio" {
				return w
			}
		}
		return nil
	}

	off := render()
	if minio(off) != nil {
		t.Fatalf("the scaffolded dev env runs MinIO with object storage off")
	}

	on := strings.Replace(string(mainK), "_object_storage = False", "_object_storage = True", 1)
	on = strings.Replace(on, "_buckets: [forge.Bucket] = []", `_buckets: [forge.Bucket] = [forge.Bucket {name = "uploads", runtime = _minio}]`, 1)
	if on == string(mainK) || !strings.Contains(on, `name = "uploads"`) || !strings.Contains(on, "_object_storage = True") {
		t.Fatalf("dev/main.k no longer carries the two lines its object-storage comment tells a user to edit:\n%s", mainK)
	}
	if err := os.WriteFile(mainPath, []byte(on), 0o644); err != nil {
		t.Fatal(err)
	}
	out := render()
	w := minio(out)
	if w == nil {
		t.Fatalf("object storage on, but no `minio` workload rendered")
	}
	rt := w["runtime"].(map[string]any)
	env, _ := rt["env"].(map[string]any)
	if rt["type"] != "compose" || rt["service"] != "minio" || env["MINIO_BUCKETS"] != "uploads" || env["MINIO_PORT"] == "" {
		t.Fatalf("minio workload runtime = %#v, want the compose service told to create `uploads`", rt)
	}
}
