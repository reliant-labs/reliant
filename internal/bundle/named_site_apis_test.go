package bundle

import (
	"reflect"
	"testing"
)

// staticSitesWithNamedAPIs is what Build hands
// release.BundleReader.CheckNamedSiteAPIs: every forge StaticSite whose spec
// declares named same-origin APIs, and nothing else.
func TestStaticSitesWithNamedAPIs(t *testing.T) {
	stream := "---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\nspec:\n  apis:\n    billing:\n      name: billing-api\n" +
		"---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: plain\nspec: {}\n" +
		"---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: empty\nspec:\n  apis: {}\n" +
		"---\napiVersion: example.com/v1\nkind: StaticSite\nmetadata:\n  name: other\nspec:\n  apis:\n    x: y\n"
	docs, err := parseStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := staticSitesWithNamedAPIs(docs), []string{"web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staticSitesWithNamedAPIs = %v, want %v", got, want)
	}
}
