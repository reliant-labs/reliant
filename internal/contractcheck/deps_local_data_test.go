package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDepsLocalDataSettingsAndHiddenCollaborators(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	must(t, writeFile(filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n"))
	must(t, mkdirAll(filepath.Join(root, "internal", "consumer")))
	must(t, writeFile(filepath.Join(root, "internal", "consumer", "service.go"), `package consumer

type Enum int32
func (e Enum) String() string { return "enum" }
func (e Enum) Validate() error { return nil }
type ClientID int
func (ClientID) Call() error { return connectToService() }
func connectToService() error { return nil }
type GlobalID int
func (GlobalID) Label() string { return singleton["key"] }
type BehavioralAlias = ClientID

type Limits struct { Rows int; Enabled bool }
type Datasource struct { Name string; Limits Limits }
func (d Datasource) Label() string { return d.Name }
type Alias = Datasource
type Rows []Alias
type Index map[string]Rows

type Callback func() error
type HiddenCallback struct { Build Callback }
type NestedCallback struct { Items map[string][]HiddenCallback }
type Client interface { Call() error }
type ClientAlias = Client
type HiddenInterface struct { Client ClientAlias }
type NestedPointer struct { Items []*Datasource }
type LoopA []LoopB
type LoopB map[string]LoopA
type BadAlias = HiddenCallback
var singleton = map[string]string{}
type ReadsGlobal struct { Name string }
func (r ReadsGlobal) Label() string { return singleton[r.Name] }

type Deps struct {
 Good map[Enum]Datasource
 GoodPrimitiveValues map[Enum]string
 BadScalar ClientID
 BadScalarMap map[string]ClientID
 BadScalarAlias BehavioralAlias
 BadScalarGlobal map[string]GlobalID
 GoodAlias []Alias
 GoodRows []Rows
 GoodIndex []Index
 BadCallback []HiddenCallback
 BadNested []NestedCallback
 BadInterface []HiddenInterface
 BadPointer []NestedPointer
 BadCycle []LoopA
 BadAlias []BadAlias
 BadGlobal []ReadsGlobal
}
`))
	findings, err := Inspect(context.Background(), root, Options{Rules: []Rule{RuleDepsAreInterfaces}})
	if err != nil {
		t.Fatal(err)
	}
	got := findingsForRule(findings, string(RuleDepsAreInterfaces))
	if len(got) != 11 {
		t.Fatalf("want eleven live/recursive shapes rejected, got %d:\n%s", len(got), AsResult(findings).FormatText())
	}
	for _, f := range got {
		if strings.Contains(f.Message, `"Good`) {
			t.Fatalf("local inert data rejected: %s", f.Message)
		}
	}
}
