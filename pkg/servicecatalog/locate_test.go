package servicecatalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const locateSrc = `package app

type Port interface {
	Get() error
}

type Set[T any] struct{}

func (s *Set[T]) Add(v T) {}

func NewPort() Port { return nil }
`

func TestLocateResolvesSymbolsAndForgetsMissingOnes(t *testing.T) {
	c := Catalog{Schema: Schema, Module: "example.com/shop",
		Processes: []Process{{Source: Source{Path: "app.go", Symbol: "Port"}}},
		Components: []Component{{
			Source: Source{Path: "app.go", Symbol: "NewPort"},
			Methods: []CodeMethod{
				{Source: Source{Path: "app.go", Symbol: "Port.Get"}},
				{Source: Source{Path: "app.go", Symbol: "Set.Add"}},
				{Source: Source{Path: "app.go", Symbol: "Gone"}},
				{Source: Source{Path: "missing.go", Symbol: "Port"}},
				{Source: Source{Path: "app.go", Line: 7}},
			},
		}},
	}
	reads := 0
	Locate(&c, func(path string) ([]byte, error) {
		reads++
		if path == "app.go" {
			return []byte(locateSrc), nil
		}
		return nil, errors.New("no such file")
	})
	if reads != 2 {
		t.Fatalf("each file must be read once, read %d times", reads)
	}
	want := []Source{{Path: "app.go", Line: 4}, {Path: "app.go", Line: 9}, {}, {}, {Path: "app.go", Line: 7}}
	for i, m := range c.Components[0].Methods {
		if m.Source != want[i] {
			t.Errorf("method %d: got %+v, want %+v", i, m.Source, want[i])
		}
	}
	if c.Processes[0].Source != (Source{Path: "app.go", Line: 3}) || c.Components[0].Source != (Source{Path: "app.go", Line: 11}) {
		t.Fatalf("decls: %+v %+v", c.Processes[0].Source, c.Components[0].Source)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "symbol") {
		t.Fatalf("a located catalog still carries symbols: %s", raw)
	}
	if _, err := DecodeLocated(raw); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFormsRoundTripAndValidate(t *testing.T) {
	committed, _ := json.Marshal(Source{Path: "app.go", Symbol: "Port.Get"})
	if string(committed) != `{"path":"app.go","symbol":"Port.Get"}` {
		t.Fatalf("committed form: %s", committed)
	}
	located, _ := json.Marshal(Source{Path: "app.go", Line: 4})
	if string(located) != `{"path":"app.go","line":4}` {
		t.Fatalf("located form: %s", located)
	}
	if unknown, _ := json.Marshal(Source{}); string(unknown) != `{"path":"","line":0}` {
		t.Fatalf("unknown form: %s", unknown)
	}
	for _, s := range []Source{{}, {Path: "a.go", Symbol: "T.M"}, {Path: "a.go", Symbol: "Café.Größe"}, {Path: "a.go", Line: 1}} {
		if !validSource(s) {
			t.Errorf("refused %+v", s)
		}
	}
	for _, s := range []Source{{Path: "a.go"}, {Path: "a.go", Symbol: "T.M", Line: 1}, {Path: "a.go", Symbol: "a.b.c"}, {Path: "a.go", Symbol: "1x"}, {Symbol: "T"}, {Line: 3}} {
		if validSource(s) {
			t.Errorf("accepted %+v", s)
		}
	}
}
