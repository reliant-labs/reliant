package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// TestShapeHostedFrontendsOf: only an OnHosted frontend's release_urls = False
// reaches the shape. A False on another runtime has no release URLs to turn off,
// and an unset or True one is the default, so neither records anything.
func TestShapeHostedFrontendsOf(t *testing.T) {
	off, on := false, true
	e := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}, ReleaseURLs: &off},
		{Name: "docs", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}, ReleaseURLs: &on},
		{Name: "blog", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}},
		{Name: "dev", Runtime: FrontendRuntime{Type: "host"}, ReleaseURLs: &off},
	}}
	got := shapeHostedFrontendsOf(e)
	want := []release.ShapeHostedFrontend{{Frontend: "web", ReleaseURLs: &off}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shapeHostedFrontendsOf = %+v, want %+v", got, want)
	}
	if got := shapeHostedFrontendsOf(&KCLEntities{}); got != nil {
		t.Errorf("no frontends: %+v, want nil", got)
	}
}
