package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// `after` unset ("after the default unit") and `after = []` ("waits for
// nothing") are different declarations; the decode must keep them apart or an
// author's `after = []` silently becomes "after base".
func TestDeliveryUnitsKeepUnsetAndEmptyApart(t *testing.T) {
	var raw []deliveryUnitRaw
	in := `[{"name":"a","after":[]},{"name":"b","after":["a"]},{"name":"c","after":null},{"name":"d"}]`
	if err := json.Unmarshal([]byte(in), &raw); err != nil {
		t.Fatal(err)
	}
	got := deliveryUnitsOf(raw)
	want := []release.DeliveryUnit{
		{Name: "a", After: []string{}},
		{Name: "b", After: []string{"a"}},
		{Name: "c"},
		{Name: "d"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if deliveryUnitsOf(nil) != nil {
		t.Error("no declarations must decode to nil")
	}
}
