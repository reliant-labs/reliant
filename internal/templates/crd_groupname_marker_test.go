package templates

import (
	"strings"
	"testing"
)

// The API-types templates must carry a package-level +groupName marker.
//
// This is a regression test for a defect that stays invisible until someone
// runs controller-gen, which is the worst possible time to find it.
//
// controller-gen resolves a CRD's API group ONLY from the +groupName marker.
// It never reads the `GroupVersion = schema.GroupVersion{Group: ...}` var these
// templates emit — that is ordinary Go, not a marker.
//
// So without the marker `controller-gen crd` still SUCCEEDS and still writes a
// file. The file just carries an empty spec.group and a metadata.name of
// "<plurals>." — a trailing dot with nothing after it — which the API server
// rejects. Nothing in the scaffold, the build, or a lint catches it.
//
// Found by running controller-gen against a scaffolded api/v1alpha1 in a real
// project: it produced `name: workspaces.` and `group: ""`.
func TestAPITypesTemplatesDeclareGroupNameMarker(t *testing.T) {
	for _, tmplPath := range []string{
		"crd/groupversion.go.tmpl",
		"operator/types.go.tmpl",
	} {
		t.Run(tmplPath, func(t *testing.T) {
			raw, err := templateFS.ReadFile(tmplPath)
			if err != nil {
				t.Fatalf("read %s: %v", tmplPath, err)
			}
			src := string(raw)

			if !strings.Contains(src, "// +groupName={{.Group}}") {
				t.Fatalf(""+
					"%s does not declare a package-level `// +groupName={{.Group}}` marker.\n"+
					"Without it `controller-gen crd` emits a CRD with an empty spec.group and "+
					"a malformed metadata.name (\"<plurals>.\"), which the API server rejects — "+
					"and it does so WITHOUT failing, so nothing in the scaffold or the build "+
					"catches it.",
					tmplPath)
			}

			// Placement matters as much as presence: controller-gen only honours
			// the marker when it is in the doc comment attached to the package
			// clause. A +groupName higher in the file, separated by a blank line,
			// is silently ignored — which reproduces the exact bug being guarded.
			idx := strings.Index(src, "\npackage ")
			if idx < 0 {
				t.Fatalf("%s has no package clause", tmplPath)
			}
			doc := src[:idx]
			if i := strings.LastIndex(doc, "\n\n"); i >= 0 {
				doc = doc[i:]
			}
			if !strings.Contains(doc, "+groupName=") {
				t.Errorf(""+
					"%s declares +groupName but NOT in the doc comment attached to the package "+
					"clause, so controller-gen ignores it. Move it into the comment block "+
					"directly above `package`, with no blank line in between.",
					tmplPath)
			}
		})
	}
}
