package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOptionalMapPkg lays down a handlers/<pkg>/ package whose Deps hold
// optional TABLES of collaborators — the shape forge accepts as a Deps field
// since deps-are-interfaces allowed a map/slice of interfaces (reliant's
// trigger handler: `Access map[string]IntegrationAccess`,
// `Senders map[string]SenderDirectory`) — beside element types whose zero
// value is not nil, a named map type, a slice and an array.
func writeOptionalMapPkg(t *testing.T, projectDir, methods string) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", "handlers", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	service := `package fixture

type Accessor interface {
	Do() error
}

type Limits struct {
	Max int
}

// Registry is a named map type: the walker resolves it to its map.
type Registry map[string]Accessor

type Deps struct {
	// forge:optional-dep
	Access map[string]Accessor
	// forge:optional-dep
	Counts map[string]int
	// forge:optional-dep
	Limits map[string]Limits
	// forge:optional-dep
	Named Registry
	// forge:optional-dep
	List []Accessor
	// forge:optional-dep
	Fixed [2]int
}

type Service struct {
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handlers.go"), []byte("package fixture\n\n"+methods), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOptionalDepsGuard_MapDeps pins what a map-typed optional dep may do
// while it is nil. Reading a nil map is safe in Go — an index yields the zero
// value, len is 0, range runs zero times, delete is a no-op — so none of
// those is flagged. Writing into one panics, so a write is. And an ELEMENT of
// a nil map is the zero value of its type: when that is nil (an interface, a
// pointer, a func…), dereferencing a looked-up element panics on every key
// while the dep is unwired, so that is flagged until a nil check — on the
// map, or on the element (a non-nil element proves the map non-nil) — or a
// comma-ok read dominates it.
func TestOptionalDepsGuard_MapDeps(t *testing.T) {
	t.Parallel()

	type want struct {
		field, method, use string
	}
	cases := []struct {
		name    string
		methods string
		want    []want // nil → expect zero findings
	}{
		{
			name: "read-into-var-not-flagged",
			methods: `
func (s *Service) Call(k string) bool {
	a := s.deps.Access[k]
	return a == nil
}
`,
		},
		{
			name: "comma-ok-read-not-flagged",
			methods: `
func (s *Service) Call(k string) bool {
	if _, gated := s.deps.Access[k]; gated {
		return true
	}
	return false
}
`,
		},
		{
			name: "len-range-delete-not-flagged",
			methods: `
func (s *Service) Call(k string) int {
	n := len(s.deps.Counts)
	for key, v := range s.deps.Counts {
		n += len(key) + v
	}
	delete(s.deps.Counts, k)
	return n + s.deps.Counts[k]
}
`,
		},
		{
			name: "element-of-non-nil-zero-type-not-flagged",
			methods: `
func (s *Service) Call(k string) int {
	return s.deps.Counts[k] + s.deps.Limits[k].Max
}
`,
		},
		{
			name: "named-map-type-read-not-flagged",
			methods: `
func (s *Service) Call(k string) bool {
	_, ok := s.deps.Named[k]
	return ok
}
`,
		},
		{
			name: "alias-of-map-then-read-not-flagged",
			methods: `
func (s *Service) Call(k string) int {
	counts := s.deps.Counts
	return counts[k]
}
`,
		},
		{
			name: "write-flagged",
			methods: `
func (s *Service) Set(k string, a Accessor) {
	s.deps.Access[k] = a
}
`,
			want: []want{{"Access", "Set", optionalUseMapWrite}},
		},
		{
			name: "op-assign-and-incdec-flagged",
			methods: `
func (s *Service) Bump(k string) {
	s.deps.Counts[k] += 2
	s.deps.Counts[k]++
}
`,
			want: []want{{"Counts", "Bump", optionalUseMapWrite}, {"Counts", "Bump", optionalUseMapWrite}},
		},
		{
			name: "named-map-type-write-flagged",
			methods: `
func (s *Service) Set(k string, a Accessor) {
	s.deps.Named[k] = a
}
`,
			want: []want{{"Named", "Set", optionalUseMapWrite}},
		},
		{
			name: "write-through-alias-flagged",
			methods: `
func (s *Service) Set(k string) {
	counts := s.deps.Counts
	counts[k] = 1
}
`,
			want: []want{{"Counts", "Set", optionalUseMapWrite}},
		},
		{
			name: "guarded-write-not-flagged",
			methods: `
func (s *Service) Set(k string, a Accessor) {
	if s.deps.Access == nil {
		return
	}
	s.deps.Access[k] = a
}
`,
		},
		{
			name: "element-deref-flagged",
			// The decision: every lookup in a nil map yields the zero
			// element, so with Access unwired this panics for EVERY k —
			// the failure this lint exists to catch.
			methods: `
func (s *Service) Call(k string) error {
	return s.deps.Access[k].Do()
}
`,
			want: []want{{"Access", "Call", optionalUseMapElement}},
		},
		{
			name: "element-alias-deref-flagged",
			methods: `
func (s *Service) Call(k string) error {
	a := s.deps.Access[k]
	return a.Do()
}
`,
			want: []want{{"Access", "Call", optionalUseMapElement}},
		},
		{
			name: "element-alias-nil-checked-not-flagged",
			// reliant's trigger shape (trigger_inbound.go, trigger_senders.go).
			methods: `
func (s *Service) Call(k string) error {
	a := s.deps.Access[k]
	if a == nil {
		return nil
	}
	return a.Do()
}
`,
		},
		{
			name: "element-comma-ok-checked-not-flagged",
			methods: `
func (s *Service) Call(k string) error {
	a, ok := s.deps.Access[k]
	if !ok {
		return nil
	}
	return a.Do()
}
`,
		},
		{
			name: "element-deref-under-map-guard-not-flagged",
			methods: `
func (s *Service) Call(k string) error {
	if s.deps.Access == nil {
		return nil
	}
	return s.deps.Access[k].Do()
}
`,
		},
		{
			name: "element-deref-under-element-guard-not-flagged",
			methods: `
func (s *Service) Call(k string) error {
	if s.deps.Access[k] != nil {
		return s.deps.Access[k].Do()
	}
	return nil
}
`,
		},
		{
			name: "named-map-element-deref-flagged",
			methods: `
func (s *Service) Call(k string) error {
	return s.deps.Named[k].Do()
}
`,
			want: []want{{"Named", "Call", optionalUseMapElement}},
		},
		{
			name: "slice-and-array-index-still-flagged",
			methods: `
func (s *Service) Call() int {
	_ = s.deps.List[0]
	return s.deps.Fixed[1]
}
`,
			want: []want{{"List", "Call", optionalUseDeref}, {"Fixed", "Call", optionalUseDeref}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectDir := t.TempDir()
			writeOptionalMapPkg(t, projectDir, tc.methods)

			got, err := collectOptionalDepsGuardFindings(projectDir)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].Field != w.field || got[i].Method != w.method || got[i].Use != w.use {
					t.Errorf("finding[%d] = %s/%s in %s, want %s/%s in %s", i, got[i].Field, got[i].Use, got[i].Method, w.field, w.use, w.method)
				}
			}
		})
	}
}

// TestOptionalDepsGuard_MapFindingWording asserts each kind of map finding
// says what actually panics and how to guard it, in both the text report and
// the --json fix_hint.
func TestOptionalDepsGuard_MapFindingWording(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	writeOptionalMapPkg(t, projectDir, `
func (s *Service) Set(k string, a Accessor) {
	s.deps.Access[k] = a
}

func (s *Service) Call(k string) error {
	return s.deps.Access[k].Do()
}
`)
	fs, err := collectOptionalDepsGuardJSON(projectDir)
	if err != nil {
		t.Fatalf("collect JSON: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("findings = %d, want 2: %+v", len(fs), fs)
	}
	write, elem := fs[0], fs[1]
	for _, needle := range []string{"writes into", "Deps.Access", "nil map panics", "Set"} {
		if !strings.Contains(write.Message, needle) {
			t.Errorf("write message missing %q: %q", needle, write.Message)
		}
	}
	for _, needle := range []string{"s.deps.Access[k]", "element", "zero value", "Call"} {
		if !strings.Contains(elem.Message, needle) {
			t.Errorf("element message missing %q: %q", needle, elem.Message)
		}
	}
	for _, needle := range []string{"comma-ok", "forge:optional-checked"} {
		if !strings.Contains(elem.FixHint, needle) {
			t.Errorf("element fix_hint missing %q: %q", needle, elem.FixHint)
		}
	}
	if !strings.Contains(write.FixHint, "forge:optional-checked") {
		t.Errorf("write fix_hint must document the suppression directive: %q", write.FixHint)
	}

	js, err := collectOptionalDepsGuardJSON(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, f := range js {
		text.WriteString(f.Message + "\n")
	}
	if !strings.Contains(text.String(), "writes into optional map dep") || !strings.Contains(text.String(), "element of optional map dep") {
		t.Errorf("the report does not distinguish map findings:\n%s", text.String())
	}
}
