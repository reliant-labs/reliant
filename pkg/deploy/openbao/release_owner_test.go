package openbao

import "testing"

func TestRenderNativeProviderReleaseOwnership(t *testing.T) {
	spec := raftSpec()
	spec.ReleaseOwner = "openbao-openbao-store"
	objects, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCount := 0
	for _, object := range objects {
		if object.GetAnnotations()["forge.dev/release-owners"] != `["openbao-openbao-store"]` {
			t.Fatalf("native %s/%s lacks explicit atomic provider owner", object.GetKind(), object.GetName())
		}
		if object.GetKind() == "Job" || object.GetKind() == "StatefulSet" || object.GetKind() == "Deployment" {
			runtimeCount++
		}
	}
	if runtimeCount < 3 {
		t.Fatalf("expected server plus converge and widen runtime resources, got %d", runtimeCount)
	}
}
