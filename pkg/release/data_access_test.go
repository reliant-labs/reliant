package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func shapeWithAccess(da *ShapeDataAccess) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}, DataAccess: da}
}

// The default is the absence of a declaration: a producer that spells it out
// encodes the same as one that says nothing, so no shape recorded before the
// field changes.
func TestDataAccessDefaultIsOmittedFromTheCanonicalShape(t *testing.T) {
	bare := shapeWithAccess(nil).Canonical()
	spelled := shapeWithAccess(&ShapeDataAccess{DBQuery: DBQueryAudited}).Canonical()
	if spelled.DataAccess != nil {
		t.Errorf("a default DataAccess survived Canonical: %+v", spelled.DataAccess)
	}
	a, _ := json.Marshal(bare)
	b, _ := json.Marshal(spelled)
	if string(a) != string(b) {
		t.Errorf("spelling the default changed the encoding:\n%s\n%s", a, b)
	}
}

// A non-default policy IS recorded, changes the canonical encoding, and
// survives a round trip through the document the control plane stores.
func TestDataAccessNonDefaultIsRecorded(t *testing.T) {
	for _, p := range []DBQueryPolicy{DBQueryOpen, DBQueryDeny} {
		s := shapeWithAccess(&ShapeDataAccess{DBQuery: p}).Canonical()
		if s.DataAccess == nil || s.DataAccess.DBQuery != p {
			t.Fatalf("%s: canonical shape lost the declaration: %+v", p, s.DataAccess)
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if bare, _ := json.Marshal(shapeWithAccess(nil).Canonical()); string(raw) == string(bare) {
			t.Errorf("%s: the policy does not move the encoding", p)
		}
		var back Shape
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.DataAccess, s.DataAccess) {
			t.Errorf("%s: round trip changed the declaration: %+v", p, back.DataAccess)
		}
	}
}

func TestDataAccessUnknownPolicyIsInvalid(t *testing.T) {
	err := shapeWithAccess(&ShapeDataAccess{DBQuery: "yolo"}).Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate = %v, want ErrInvalid", err)
	}
	if err := shapeWithAccess(&ShapeDataAccess{DBQuery: DBQueryOpen}).Validate(); err != nil {
		t.Errorf("a valid policy was refused: %v", err)
	}
}

// Absence reads as audited, never as open: a reader who finds no declaration
// must not conclude queries are unrestricted.
func TestDataAccessAbsenceResolvesToAudited(t *testing.T) {
	var none *ShapeDataAccess
	if got := none.Resolved().DBQuery; got != DBQueryAudited {
		t.Errorf("absent resolves to %q, want audited", got)
	}
	if !none.IsDefault() || (&ShapeDataAccess{DBQuery: DBQueryOpen}).IsDefault() {
		t.Error("IsDefault disagrees with the default")
	}
}
