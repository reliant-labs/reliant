package orm

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// The pairings below are what a json/jsonb column stores for a structured
// proto field. forgepb is used as the fixture package because it is the
// only compiled proto in this module; the shapes it happens to carry are
// the ones under test — a repeated message (EntityOptions.indexes), a
// repeated scalar inside it (IndexDef.fields), and an enum
// (ConfigFieldOptions.role).

func TestMarshalJSONBList_RoundTripsRepeatedMessages(t *testing.T) {
	in := []*forgepb.IndexDef{
		{Name: "idx_email", Fields: []string{"email"}, Unique: true},
		{Name: "idx_name", Fields: []string{"first", "last"}},
	}

	s, err := MarshalJSONBList(in)
	if err != nil {
		t.Fatalf("MarshalJSONBList: %v", err)
	}
	if !json.Valid([]byte(s)) {
		t.Fatalf("stored text is not valid JSON: %s", s)
	}

	var out []*forgepb.IndexDef
	if err := UnmarshalJSONBList(s, &out); err != nil {
		t.Fatalf("UnmarshalJSONBList: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("round trip lost elements: got %d, want %d (%s)", len(out), len(in), s)
	}
	for i := range in {
		if !proto.Equal(in[i], out[i]) {
			t.Errorf("element %d changed: %v -> %v", i, in[i], out[i])
		}
	}
}

// TestMarshalJSONBList_EmptyIsAnArrayNotNull pins the value a NOT NULL
// jsonb column with CHECK (jsonb_typeof(col) = 'array') can actually hold.
// encoding/json renders a nil slice as `null`, which fails that CHECK and
// aborts the INSERT.
func TestMarshalJSONBList_EmptyIsAnArrayNotNull(t *testing.T) {
	for _, in := range [][]*forgepb.IndexDef{nil, {}} {
		s, err := MarshalJSONBList(in)
		if err != nil {
			t.Fatalf("MarshalJSONBList(%v): %v", in, err)
		}
		if s != "[]" {
			t.Errorf("empty list stored as %q, want []", s)
		}
	}
	var out []*forgepb.IndexDef
	for _, absent := range []string{"", "null", "[]"} {
		out = nil
		if err := UnmarshalJSONBList(absent, &out); err != nil {
			t.Errorf("UnmarshalJSONBList(%q): %v", absent, err)
		}
		if len(out) != 0 {
			t.Errorf("UnmarshalJSONBList(%q) produced %d elements", absent, len(out))
		}
	}
}

// TestJSONBEncodingIsSQLReadable pins the two encoder choices a SQL author
// depends on: keys are the PROTO names (so `col ->> 'soft_delete'` is the
// spelling that works, matching the columns around it), and every declared
// field is present (so an expression never has to distinguish an absent key
// from a zero value).
func TestJSONBEncodingIsSQLReadable(t *testing.T) {
	s, err := MarshalJSONBMessage(&forgepb.EntityOptions{Table: "orders"})
	if err != nil {
		t.Fatalf("MarshalJSONBMessage: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("stored text is not a JSON object: %s", s)
	}
	if _, ok := doc["soft_delete"]; !ok {
		t.Errorf("proto-name key soft_delete missing (camelCase leaked?): %s", s)
	}
	if _, ok := doc["softDelete"]; ok {
		t.Errorf("camelCase key softDelete present; keys must be proto names: %s", s)
	}
	// A zero-valued field is still written — that is EmitUnpopulated.
	if v, ok := doc["timestamps"]; !ok || v != false {
		t.Errorf("unpopulated field must still be present as its zero value: %s", s)
	}
}

// TestUnmarshalJSONBIsLoudOnCorruptDocuments pins the failure half. A
// stored document the current proto cannot represent is corrupt data; the
// read must fail rather than return a message with the value silently
// dropped. That is the same rule the generated enum read path follows.
func TestUnmarshalJSONBIsLoudOnCorruptDocuments(t *testing.T) {
	cases := []struct {
		name, stored string
		read         func(string) error
	}{
		{"unknown key", `[{"name":"x","gone":1}]`, func(s string) error {
			var out []*forgepb.IndexDef
			return UnmarshalJSONBList(s, &out)
		}},
		{"array where an object belongs", `["enterprise","free"]`, func(s string) error {
			var out *forgepb.EntityOptions
			return UnmarshalJSONBMessage(s, &out)
		}},
		{"object where an array belongs", `{"label":"trial"}`, func(s string) error {
			var out []*forgepb.IndexDef
			return UnmarshalJSONBList(s, &out)
		}},
		{"scalars where messages belong", `["enterprise","free"]`, func(s string) error {
			var out []*forgepb.IndexDef
			return UnmarshalJSONBList(s, &out)
		}},
		{"not JSON at all", `sample_line_items_2`, func(s string) error {
			var out []*forgepb.IndexDef
			return UnmarshalJSONBList(s, &out)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.read(tc.stored)
			if err == nil {
				t.Fatalf("reading %s must fail, not silently drop the value", tc.stored)
			}
			if !strings.Contains(err.Error(), "orm:") {
				t.Errorf("error should name the layer that failed; got %v", err)
			}
		})
	}
}

func TestMarshalJSONBMessage_NilAndRoundTrip(t *testing.T) {
	// A nil message stores the message's zero DOCUMENT, not SQL-invalid
	// text: every declared field at its zero value, which is what proto3
	// says an absent message means and what the read path reconstructs.
	s, err := MarshalJSONBMessage((*forgepb.IndexDef)(nil))
	if err != nil {
		t.Fatalf("MarshalJSONBMessage(nil): %v", err)
	}
	var zero *forgepb.IndexDef
	if err := UnmarshalJSONBMessage(s, &zero); err != nil {
		t.Fatalf("a nil message must store a READABLE document; got %q: %v", s, err)
	}
	if zero == nil || !proto.Equal(zero, &forgepb.IndexDef{}) {
		t.Errorf("a nil message must read back as the zero message; got %v from %q", zero, s)
	}

	in := &forgepb.ConfigFieldOptions{
		EnvVar: "DATABASE_URL",
		Role:   forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_MODE,
	}
	stored, err := MarshalJSONBMessage(in)
	if err != nil {
		t.Fatalf("MarshalJSONBMessage: %v", err)
	}
	// Enums store their declared VALUE NAME, exactly as an enum COLUMN
	// does — the wire number encoding/json would print is unreadable in
	// psql and breaks on a renumber.
	if !strings.Contains(stored, "CONFIG_FIELD_ROLE_MODE") {
		t.Errorf("enum must store its value name; got %s", stored)
	}
	var out *forgepb.ConfigFieldOptions
	if err := UnmarshalJSONBMessage(stored, &out); err != nil {
		t.Fatalf("UnmarshalJSONBMessage: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Errorf("round trip changed the message: %v -> %v", in, out)
	}

	// An absent column leaves the wire field nil rather than allocating.
	for _, absent := range []string{"", "null"} {
		var nilOut *forgepb.ConfigFieldOptions
		if err := UnmarshalJSONBMessage(absent, &nilOut); err != nil {
			t.Errorf("UnmarshalJSONBMessage(%q): %v", absent, err)
		}
		if nilOut != nil {
			t.Errorf("UnmarshalJSONBMessage(%q) allocated a message", absent)
		}
	}
}

func TestJSONBScalars_RoundTrip(t *testing.T) {
	in := []string{"alpha", "beta"}
	s, err := MarshalJSONBScalars(in)
	if err != nil {
		t.Fatalf("MarshalJSONBScalars: %v", err)
	}
	if s != `["alpha","beta"]` {
		t.Errorf("stored %q", s)
	}
	var out []string
	if err := UnmarshalJSONBScalars(s, &out); err != nil {
		t.Fatalf("UnmarshalJSONBScalars: %v", err)
	}
	if len(out) != 2 || out[0] != "alpha" || out[1] != "beta" {
		t.Errorf("round trip changed the slice: %v", out)
	}
	if s, err := MarshalJSONBScalars([]string(nil)); err != nil || s != "[]" {
		t.Errorf("a nil slice must store [], got %q (%v)", s, err)
	}
}
