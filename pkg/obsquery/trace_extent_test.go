package obsquery

import (
	"strings"
	"testing"
)

func TestTraceExtentIsOneParameterizedAggregate(t *testing.T) {
	st, err := TraceExtent{Scope: scope(), TraceID: "6CDCC2B8D409E05528B82B2687A58AF2"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, "6cdcc2b8d409e05528b82b2687a58af2")
	if !hasValue(st.Params, "6cdcc2b8d409e05528b82b2687a58af2") || !strings.Contains(st.SQL, "count() AS spans") || strings.Contains(st.SQL, "GROUP BY") {
		t.Errorf("extent: %s %v", st.SQL, st.Params)
	}
	if _, err := (TraceExtent{Scope: scope(), TraceID: "x' OR 1=1"}).Compile(); !isInvalid(err) {
		t.Errorf("hostile trace id accepted")
	}
}
