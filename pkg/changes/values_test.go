package changes

import (
	"encoding/json"
	"testing"
)

func valued(raw string) Result {
	if raw == "" {
		return Result{Reason: ReasonUnknown}
	}
	return Result{Variant: "v", Value: json.RawMessage(raw), Reason: ReasonEveryone}
}

func TestTypedAccessors(t *testing.T) {
	if !Bool(valued(`true`), false) || Bool(valued(``), false) || !Bool(valued(`"true"`), true) {
		t.Error("Bool: a JSON boolean is its value; no value or another type is the default")
	}
	if String(valued(`"blue"`), "x") != "blue" || String(valued(`3`), "x") != "x" || String(valued(``), "x") != "x" {
		t.Error("String: a JSON string is its value; no value or another type is the default")
	}
	if Float64(valued(`2.5`), 1) != 2.5 || Float64(valued(`"2.5"`), 1) != 1 || Float64(valued(``), 1) != 1 {
		t.Error("Float64: a JSON number is its value; no value or another type is the default")
	}

	type limits struct {
		MaxMB int `json:"max_mb"`
	}
	var l limits
	if !Decode(valued(`{"max_mb":50}`), &l) || l.MaxMB != 50 {
		t.Errorf("Decode: got %+v", l)
	}
	l = limits{MaxMB: 9}
	if Decode(valued(``), &l) || l.MaxMB != 9 {
		t.Errorf("Decode with no value must report false and leave v alone, got %+v", l)
	}
	if Decode(valued(`"text"`), &l) {
		t.Error("Decode of a value that does not fit must report false")
	}
}
