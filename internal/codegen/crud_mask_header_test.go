package codegen

import (
	"strings"
	"testing"
)

// A service with a masked Update tells its handler author, in the header of
// the handlers_crud.go they edit, that a mask path is a whole column and which
// pkg/crud helper answers "does this update write column X". Without it a
// hand-written validation hook matched paths itself and treated "spec.x" as
// not touching spec (friction: ranger's touchesSpec).
func TestCRUDShim_HeaderNamesMaskTouchesForAMaskedUpdate(t *testing.T) {
	_, dir := delegatePackage(t)
	if !exportedSurface(t, dir)["MaskTouches"] {
		t.Fatal("the header names crud.MaskTouches, but pkg/crud exports no MaskTouches")
	}

	masked := crudShimHeader(CRUDTemplateData{
		Module:      "example.com/x",
		CRUDMethods: []CRUDMethodTemplateData{{Operation: "update", UpdateMaskField: "UpdateMask"}},
	})
	flat := strings.Join(strings.Fields(strings.ReplaceAll(masked, "//", " ")), " ")
	for _, want := range []string{"names a WHOLE column", "crud.MaskTouches(paths,"} {
		if !strings.Contains(flat, want) {
			t.Errorf("a masked Update's shim header must say %q:\n%s", want, masked)
		}
	}

	for name, data := range map[string]CRUDTemplateData{
		"update without a mask": {Module: "example.com/x", CRUDMethods: []CRUDMethodTemplateData{{Operation: "update"}}},
		"no update":             {Module: "example.com/x", CRUDMethods: []CRUDMethodTemplateData{{Operation: "create"}}},
	} {
		if h := crudShimHeader(data); strings.Contains(h, "MaskTouches") {
			t.Errorf("%s: the header names MaskTouches with no update_mask to apply it to:\n%s", name, h)
		}
	}
}
