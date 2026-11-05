package kcl

import (
	"bufio"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// TestDeployTargetsMatchSchemaSource is the anti-drift test.
//
// It re-derives the expected target set from schema.k by a DIFFERENT and
// deliberately dumber route than the parser under test — a grep for
// `runtime?:` lines and a split on `|` — and asserts the two agree. Add a
// target to a union in schema.k and this test keeps passing (both sides see
// it). Add one and break the parser, and the two disagree and this fails.
//
// The point is that nothing in this file writes down a target NAME. A test
// asserting `[]string{"FirebaseHosting", "K8sCluster"}` would be the exact
// hand-maintained list the reflection exists to eliminate: it would fail on
// the commit that legitimately adds StaticSite, teaching whoever hits it to
// paste the new name in rather than to check the parser.
func TestDeployTargetsMatchSchemaSource(t *testing.T) {
	want := grepDeployUnions(t, "schema.k", "workload.k")
	if len(want) == 0 {
		t.Fatal("no `runtime?:` union found — the grep oracle is broken, not the parser")
	}

	targets, err := DeployTargets()
	if err != nil {
		t.Fatalf("DeployTargets: %v", err)
	}
	got := map[string][]string{}
	for _, tgt := range targets {
		for _, w := range tgt.Workloads {
			got[w] = append(got[w], tgt.Name)
		}
	}
	for _, v := range got {
		sort.Strings(v)
	}

	if len(got) != len(want) {
		t.Fatalf("owning schemas: got %v, want %v", keys(got), keys(want))
	}
	for owner, wantMembers := range want {
		sort.Strings(wantMembers)
		gotMembers := got[owner]
		if strings.Join(gotMembers, ",") != strings.Join(wantMembers, ",") {
			t.Errorf("%s deploy/runtime union: got %v, want %v", owner, gotMembers, wantMembers)
		}
	}
}

// TestDeployTargetsResolveToSchemas asserts every discovered target resolves
// to a real schema declaration with a location, a docstring and fields. A
// union member that failed to resolve is still REPORTED (so the listing
// never lies about what the union accepts) — this is what catches that
// silent degradation, which would otherwise look like a working command
// printing a blank line.
func TestDeployTargetsResolveToSchemas(t *testing.T) {
	targets, err := DeployTargets()
	if err != nil {
		t.Fatal(err)
	}
	for _, tgt := range targets {
		if tgt.File == "" || tgt.Line == 0 {
			t.Errorf("%s: union member did not resolve to a schema declaration", tgt.Name)
			continue
		}
		if tgt.Doc == "" {
			t.Errorf("%s: no docstring — the docstrings are most of the value of this listing", tgt.Name)
		}
		if len(tgt.Fields) == 0 {
			t.Errorf("%s: no fields parsed at %s:%d", tgt.Name, tgt.File, tgt.Line)
		}
		if len(tgt.Workloads) == 0 {
			t.Errorf("%s: no owning workload schema", tgt.Name)
		}
	}
}

// TestDeployTargetsForIsPerWorkload pins the distinction that is load-bearing
// for the deploy warning's hint text: Service-side and Frontend-side unions
// differ, so the hint must be built from the FRONTEND union or it will offer
// a target the schema rejects.
func TestDeployTargetsForIsPerWorkload(t *testing.T) {
	fe, err := DeployTargetsFor("Frontend")
	if err != nil {
		t.Fatal(err)
	}
	if len(fe) == 0 {
		t.Fatal("Frontend declares no deploy targets — schema.k changed shape")
	}

	all, err := DeployTargets()
	if err != nil {
		t.Fatal(err)
	}
	// Derived, not listed: a target valid ONLY on the other union must not
	// appear in the Frontend set.
	inFrontend := map[string]bool{}
	for _, n := range fe {
		inFrontend[n] = true
	}
	for _, tgt := range all {
		frontendOK := false
		for _, w := range tgt.Workloads {
			if w == "Frontend" {
				frontendOK = true
			}
		}
		if frontendOK != inFrontend[tgt.Name] {
			t.Errorf("%s: DeployTargetsFor(Frontend)=%v but Workloads=%v",
				tgt.Name, inFrontend[tgt.Name], tgt.Workloads)
		}
	}

	if none, err := DeployTargetsFor("NoSuchSchema"); err != nil || len(none) != 0 {
		t.Errorf("unknown workload schema: got %v, %v; want empty, nil", none, err)
	}
}

// TestParsesRealSchemaShapes guards the parser against the constructs the
// real schema.k actually contains, which is where a naive line parser breaks:
// multi-line docstrings containing indented KCL EXAMPLES that look exactly
// like field declarations, check: blocks with backslash continuations,
// comments between fields, and defaulted vs optional fields.
func TestParsesRealSchemaShapes(t *testing.T) {
	src := `schema Decoy:
    """A docstring whose body contains an example.

        forge.Service { name = "trader", deploy = _prod.deploy }

      * bullet: not a field
    """
    real_field: str
    # a comment between fields
    defaulted: str = "yes"
    optional?: int
    container: [{str: any}] = []
    runtime?: Alpha | Beta

    check:
        real_field, "required"
        optional == Undefined or optional > 0, \
            "a wrapped condition that must not parse as a field"

schema Alpha:
    """Alpha doc."""
    a: str

schema Beta:
    """Beta doc line one.

    More prose.
    """
    b?: str
`
	targets, err := deployTargetsFrom(fstest.MapFS{"schema.k": {Data: []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(targets), targets)
	}
	if targets[0].Name != "Alpha" || targets[0].Doc != "Alpha doc." {
		t.Errorf("Alpha: %+v", targets[0])
	}
	if targets[1].Name != "Beta" || targets[1].Doc != "Beta doc line one." {
		t.Errorf("Beta: %+v", targets[1])
	}

	schemas, _, err := parseModule(fstest.MapFS{"schema.k": {Data: []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	decoy := schemas["Decoy"]
	var names []string
	for _, f := range decoy.fields {
		names = append(names, f.Name)
	}
	wantFields := "real_field,defaulted,optional,container,runtime"
	if strings.Join(names, ",") != wantFields {
		t.Errorf("Decoy fields: got %v, want %s", names, wantFields)
	}

	byName := map[string]SchemaField{}
	for _, f := range decoy.fields {
		byName[f.Name] = f
	}
	if f := byName["defaulted"]; f.Type != "str" || f.Default != `"yes"` || f.Optional || f.Required() {
		t.Errorf("defaulted: %+v", f)
	}
	if f := byName["optional"]; f.Type != "int" || !f.Optional || f.Default != "" || f.Required() {
		t.Errorf("optional: %+v", f)
	}
	if f := byName["real_field"]; !f.Required() {
		t.Errorf("real_field should be required: %+v", f)
	}
	if f := byName["container"]; f.Type != "[{str: any}]" || f.Default != "[]" {
		t.Errorf("container: %+v", f)
	}
}

// TestNonUnionDeployFieldYieldsNoTargets: a `deploy` field that is not a
// union of bare schema names contributes nothing, rather than a garbage
// target named "str" or "[EnvVar]".
func TestNonUnionDeployFieldYieldsNoTargets(t *testing.T) {
	for _, typ := range []string{"str", "[EnvVar]", "{str: any}", "Alpha"} {
		src := "schema W:\n    runtime?: " + typ + "\n"
		targets, err := deployTargetsFrom(fstest.MapFS{"schema.k": {Data: []byte(src)}})
		if err != nil {
			t.Fatal(err)
		}
		if len(targets) != 0 {
			t.Errorf("runtime?: %s yielded %+v, want none", typ, targets)
		}
	}
}

var (
	reDeployUnionLine = regexp.MustCompile(`^    runtime\??:\s*(.+)$`)
	reAliasLine       = regexp.MustCompile(`^type\s+(\w+)\s*=\s*(.+)$`)
)

// grepDeployUnions is the independent oracle: a dumb line grep over files in
// the real module, owner-attributed by the nearest preceding `schema X:`,
// with `type X = A | B` aliases substituted textually.
func grepDeployUnions(t *testing.T, names ...string) map[string][]string {
	t.Helper()
	var b strings.Builder
	for _, name := range names {
		f, err := Module.Open(name)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			b.WriteString(sc.Text())
			b.WriteByte('\n')
		}
		_ = f.Close()
	}
	src := b.String()
	for _, line := range strings.Split(src, "\n") {
		if m := reAliasLine.FindStringSubmatch(line); m != nil {
			src = regexp.MustCompile(`(?m)^(    runtime\??:\s*)`+m[1]+`\s*$`).ReplaceAllString(src, "${1}"+m[2])
		}
	}
	return grepDeployUnionsSrc(t, src)
}

func grepDeployUnionsSrc(t *testing.T, src string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	owner := ""
	for _, line := range strings.Split(src, "\n") {
		if m := reSchemaDecl.FindStringSubmatch(line); m != nil {
			owner = m[1]
			continue
		}
		m := reDeployUnionLine.FindStringSubmatch(line)
		if m == nil || owner == "" || !strings.Contains(m[1], "|") {
			continue
		}
		var members []string
		for _, p := range strings.Split(m[1], "|") {
			members = append(members, strings.TrimSpace(p))
		}
		out[owner] = members
	}
	return out
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
