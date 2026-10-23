package cli

// `forge project libraries --signatures` must answer, from the real
// package source, the question 36 of a wave's 53 unit bash calls answered
// with `go doc`: what does svcerr.WithReason take, what are the fields of
// tdd.Case, what is on orm.Context.
//
// Two properties are worth a test here, and they are different properties:
//
//  1. the output carries REAL signatures — not names, not a summary — and
//     they come from whatever source it is pointed at rather than from
//     anything written down in this repo (TestReadPackageSignatures_*);
//  2. the output is COMPLETE — a package that gains an exported symbol
//     gains a line here, with no edit anywhere
//     (TestSignatures_CoverEveryExportedSymbol).
//
// (2) is the anti-drift guard, and its oracle is deliberately not this
// package's own code: it is `go doc -all`, the go toolchain's independent
// answer to the same question, parsed out of its own output format. A
// guard that asked go/ast whether go/ast had missed something would pass
// vacuously forever.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------
// (1) real signatures, derived from the source it is pointed at
// ---------------------------------------------------------------------

// TestReadPackageSignatures_DerivesFromTheSourceItIsGiven is the proof
// that nothing here is transcribed: the fixture is written at test time,
// so no list in this repo could contain its answers. Change the fixture
// and the output changes with it — which is exactly the property a
// hand-written API digest cannot have.
func TestReadPackageSignatures_DerivesFromTheSourceItIsGiven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `package fixture

import "context"

// Doc prose that must not appear in the output.
const Reason = "why"

var ErrNope = errors.New("nope")

// WithReason is documented at length, and none of it belongs in a brief.
func WithReason(err error, code string) error { return err }

func unexportedHelper() {}

// RPCCase is the row shape.
type RPCCase struct {
	// Name is documented.
	Name    string
	WantErr int
	hidden  string
}

type Runner interface {
	Run(ctx context.Context) error
	private()
}

func (r RPCCase) Describe() string { return r.Name }

func (r RPCCase) hiddenMethod() {}

type unexportedType struct{ Name string }

func (u unexportedType) Exported() {}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// A _test.go file in the same directory is not API and must not leak.
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"),
		[]byte("package fixture\n\nfunc TestLeak() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := readPackageSignatures(dir)
	if err != nil {
		t.Fatalf("readPackageSignatures: %v", err)
	}
	got := map[string]string{}
	var all strings.Builder
	for _, s := range syms {
		got[s.Name] = s.Decl
		all.WriteString(s.Decl)
		all.WriteString("\n")
	}

	// The signature, in full. A name alone is what the terse inventory
	// already gave, and it is what sent four threads to `go doc`.
	if got["WithReason"] != "func WithReason(err error, code string) error" {
		t.Errorf("WithReason decl = %q, want the full signature", got["WithReason"])
	}
	// A struct's exported fields ARE its API. `go doc -short` renders this
	// as `struct{ ... }`, which is why RPCCase was fetched by hand three
	// times in one wave.
	for _, want := range []string{"Name    string", "WantErr int"} {
		if !strings.Contains(got["RPCCase"], want) {
			t.Errorf("RPCCase decl is missing %q:\n%s", want, got["RPCCase"])
		}
	}
	if !strings.Contains(got["Runner"], "Run(ctx context.Context) error") {
		t.Errorf("Runner decl is missing its method:\n%s", got["Runner"])
	}
	if got["RPCCase.Describe"] != "func (r RPCCase) Describe() string" {
		t.Errorf("method decl = %q", got["RPCCase.Describe"])
	}
	if got["Reason"] != `const Reason = "why"` {
		t.Errorf("const decl = %q — a const's value is frequently the API", got["Reason"])
	}
	if !strings.Contains(got["ErrNope"], "ErrNope") {
		t.Errorf("var decl = %q", got["ErrNope"])
	}

	// Unexported everything is unreachable from a project: printing it
	// costs a line and buys nothing callable.
	for _, unwanted := range []string{
		"unexportedHelper", "hidden ", "private()", "hiddenMethod", "unexportedType", "TestLeak",
	} {
		if strings.Contains(all.String(), unwanted) {
			t.Errorf("output leaked the unexported/test symbol %q:\n%s", unwanted, all.String())
		}
	}
	// Prose is the half this exists to omit — the synopsis line above each
	// block is the description, and a brief that carries the doc comments
	// too is the brief that displaces its own task.
	if strings.Contains(all.String(), "//") {
		t.Errorf("doc prose leaked into the signatures:\n%s", all.String())
	}
}

// TestSignatures_AnswerTheQuestionsTheWaveAsked pins the three lookups the
// measured fan-out wave actually spent its bash calls on, against the real
// forge/pkg. If any of these stops being answerable from this command's
// output, the command has stopped paying for itself.
func TestSignatures_AnswerTheQuestionsTheWaveAsked(t *testing.T) {
	t.Parallel()
	spec := repoLibrariesSpec(t, "svcerr,tdd,orm.Context")

	byName := map[string]LibrarySpec{}
	for _, p := range spec.Packages {
		byName[p.Name] = p
	}
	find := func(pkg, sym string) string {
		t.Helper()
		for _, s := range byName[pkg].Symbols {
			if s.Name == sym {
				return s.Decl
			}
		}
		t.Fatalf("%s.%s missing from --signatures output", pkg, sym)
		return ""
	}

	// `go doc .../svcerr` — four separate threads.
	if decl := find("svcerr", "WithReason"); !strings.HasPrefix(decl, "func WithReason(err error, code string) error") {
		t.Errorf("svcerr.WithReason = %q", decl)
	}
	// `go doc .../tdd RPCCase` — three separate threads. RPCCase is an
	// alias, so the fields live on Case; both must be reachable.
	if decl := find("tdd", "RPCCase"); !strings.Contains(decl, "= Case[Req, Resp]") {
		t.Errorf("tdd.RPCCase = %q", decl)
	}
	for _, want := range []string{"WantErr connect.Code", "Check func(", "Ctx context.Context"} {
		if decl := find("tdd", "Case"); !strings.Contains(decl, want) {
			t.Errorf("tdd.Case is missing %q:\n%s", want, decl)
		}
	}
	// `go doc .../orm Context` — three separate threads.
	for _, want := range []string{"Bun() bun.IDB", "RunTransaction(ctx context.Context, policy TxPolicy, fn func(Context) error) error"} {
		if decl := find("orm", "Context"); !strings.Contains(decl, want) {
			t.Errorf("orm.Context is missing %q:\n%s", want, decl)
		}
	}
}

// ---------------------------------------------------------------------
// (2) the anti-drift guard
// ---------------------------------------------------------------------

// TestSignatures_CoverEveryExportedSymbol fails when forge/pkg exports
// something this command does not print.
//
// The oracle is `go doc -all`, run against the same packages: an
// independent implementation, in another tool, reading the same source. A
// symbol it reports and this command omits is drift, and drift is the
// whole failure mode — a caller who trusts an incomplete API list writes
// the missing half by hand, which is worse than having no list at all.
//
// This covers EVERY package, not a chosen few, so a new forge/pkg package
// or a new exported func in an old one is caught with no edit here.
func TestSignatures_CoverEveryExportedSymbol(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go doc over every forge/pkg package; runs in task test")
	}
	t.Parallel()
	pkgDir := repoPkgDir(t)
	pkgs, err := readForgePkgPackages(pkgDir)
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}
	if len(pkgs) < 10 {
		t.Fatalf("only %d packages — the oracle would pass vacuously", len(pkgs))
	}

	var wg sync.WaitGroup
	// Bounded: this is 24 `go doc` processes, and unbounded is unkind to a
	// dev machine that is also running a build.
	sem := make(chan struct{}, 4)
	for _, p := range pkgs {
		wg.Add(1)
		go func(p LibrarySpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			want := goDocDeclarations(t, pkgDir, p.Name)
			if len(want) == 0 {
				return // a package may legitimately export nothing
			}
			syms, rerr := readPackageSignatures(p.Dir)
			if rerr != nil {
				t.Errorf("%s: readPackageSignatures: %v", p.Name, rerr)
				return
			}
			byName := map[string]string{}
			var packageLevel strings.Builder
			for _, s := range syms {
				byName[s.Name] = s.Decl
				if s.Kind == "const" || s.Kind == "var" {
					packageLevel.WriteString(s.Decl)
					packageLevel.WriteString("\n")
				}
			}
			for _, d := range want {
				// A member is checked against ITS OWN declaration, not
				// against the package's text. Checking the whole package
				// would let a struct collapsed to `struct{}` pass on the
				// strength of a field of the same name somewhere else —
				// which is exactly the `go doc -short` shape this exists
				// to beat.
				if d.Owner != "" {
					decl, ok := byName[d.Owner]
					if !ok {
						continue // the owner itself is reported below
					}
					if !containsIdentifier(decl, d.Name) {
						t.Errorf("forge/pkg/%s: %s.%s exists (go doc says so) but --signatures prints %s as:\n%s\n"+
							"That is API drift: a caller trusting this declaration would not know %s exists.",
							p.Name, d.Owner, d.Name, d.Owner, decl, d.Name)
					}
					continue
				}
				if _, ok := byName[d.Name]; ok {
					continue
				}
				// Members of a const/var group are package-level but are
				// not symbols of their own — the group is.
				if containsIdentifier(packageLevel.String(), d.Name) {
					continue
				}
				t.Errorf("forge/pkg/%s exports %s (go doc says so) but --signatures does not print it.\n"+
					"That is API drift: a caller trusting this list would write %s by hand.",
					p.Name, d.Name, d.Name)
			}
		}(p)
	}
	wg.Wait()
}

// goDocDeclaration is one exported name go doc reports. Owner is empty for
// a package-level declaration and is the type name for a struct field or
// an interface method, so each can be checked where it actually lives.
type goDocDeclaration struct {
	Owner string
	Name  string
}

// goDocDeclarations is the independent oracle: every exported name `go doc
// -all` declares for a package, attributed to its owner.
//
// It parses go doc's own output rather than the source, on purpose — an
// oracle that asked go/ast whether go/ast had missed something would pass
// vacuously forever. The shapes it reads are the ones go doc has emitted
// for a decade:
//
//	func Name(...)              a package-level function
//	func (r Recv) Name(...)     a method, recorded as Recv.Name
//	type Name ...               a type; `struct {` / `interface {` opens a
//	                            block whose members belong to it
//	const Name = ...            a single const or var
//	const ( … )                 a group whose tab-indented members are
//	                            themselves package-level
//	<tab>Name ...               a member of whichever block is open
func goDocDeclarations(t *testing.T, moduleDir, pkg string) []goDocDeclaration {
	t.Helper()
	cmd := exec.Command("go", "doc", "-all", "./"+pkg)
	cmd.Dir = moduleDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go doc -all ./%s in %s: %v", pkg, moduleDir, err)
	}
	seen := map[goDocDeclaration]bool{}
	var decls []goDocDeclaration
	add := func(owner, name string) {
		d := goDocDeclaration{Owner: owner, Name: name}
		if name == "" || !isExportedName(name) || seen[d] {
			return
		}
		seen[d] = true
		decls = append(decls, d)
	}

	openBlock := "" // the type whose body is being read, "" for none
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "}"), strings.HasPrefix(line, ")"):
			openBlock = ""
		case strings.HasPrefix(line, "\t"):
			// go doc indents its prose with spaces, so a leading tab is
			// always code — except for the comments it copies verbatim,
			// and nested composite literals at two tabs.
			body := strings.TrimPrefix(line, "\t")
			if strings.HasPrefix(body, "//") || strings.HasPrefix(body, "\t") {
				continue
			}
			for _, n := range leadingNameList(body) {
				add(openBlock, n)
			}
		case strings.HasPrefix(line, "func ("):
			if m := goDocMethodRE.FindStringSubmatch(line); m != nil {
				add("", m[1]+"."+m[2])
			}
		case strings.HasPrefix(line, "func "), strings.HasPrefix(line, "type "),
			strings.HasPrefix(line, "const "), strings.HasPrefix(line, "var "):
			_, rest, _ := strings.Cut(line, " ")
			name := leadingIdentifier(rest)
			add("", name)
			if strings.HasPrefix(line, "type ") && strings.HasSuffix(line, "{") {
				openBlock = name
			}
		}
	}
	return decls
}

// goDocMethodRE captures a method's receiver type and its name, with the
// pointer and any type parameter list stripped: `func (r *Repo[M]) Get(…)`
// is Repo.Get.
var goDocMethodRE = regexp.MustCompile(`^func \(\w+ \*?([A-Za-z_]\w*)[^)]*\) ([A-Za-z_]\w*)`)

var identifierHeadRE = regexp.MustCompile(`^[A-Za-z_]\w*`)

// leadingIdentifier is the identifier a declaration line opens with, with
// any type parameter list ignored.
func leadingIdentifier(s string) string {
	return identifierHeadRE.FindString(strings.TrimSpace(s))
}

// leadingNameList reads the comma-separated names a field or group member
// line opens with: `Name, Description string` declares two.
func leadingNameList(s string) []string {
	head, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	var out []string
	for _, part := range strings.Split(head, ",") {
		if n := leadingIdentifier(part); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func isExportedName(n string) bool {
	return n != "" && n[0] >= 'A' && n[0] <= 'Z'
}

// containsIdentifier reports whether name appears in text as a whole
// identifier rather than as a substring of a longer one.
func containsIdentifier(text, name string) bool {
	for i := 0; ; {
		idx := strings.Index(text[i:], name)
		if idx < 0 {
			return false
		}
		start := i + idx
		end := start + len(name)
		if !identifierChar(text, start-1) && !identifierChar(text, end) {
			return true
		}
		i = end
	}
}

func identifierChar(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---------------------------------------------------------------------
// selectors, output shape, and the guidance that used to send them away
// ---------------------------------------------------------------------

// repoLibrariesSpec builds the real spec against this checkout's pkg/ and
// attaches the requested signatures.
func repoLibrariesSpec(t *testing.T, selectors string) LibrariesSpec {
	t.Helper()
	dir := repoPkgDir(t)
	pkgs, err := readForgePkgPackages(dir)
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}
	spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
	if err := attachSignatures(&spec, selectors); err != nil {
		t.Fatalf("attachSignatures(%q): %v", selectors, err)
	}
	return spec
}

// TestAttachSignatures_SelectorScope: a package selector takes the whole
// package, a symbol selector takes one declaration AND the methods on it,
// and an unselected package stays terse. Scope is the caller's decision —
// this is the mechanism that honours it.
func TestAttachSignatures_SelectorScope(t *testing.T) {
	t.Parallel()
	spec := repoLibrariesSpec(t, "svcerr,money.Money,"+forgePkgModule+"/tdd")

	selected := map[string]int{}
	for _, p := range spec.Packages {
		if len(p.Symbols) > 0 {
			selected[p.Name] = len(p.Symbols)
		}
	}
	for _, want := range []string{"svcerr", "money", "tdd"} {
		if selected[want] == 0 {
			t.Errorf("%s was selected but carries no symbols (selected: %v)", want, selected)
		}
	}
	if len(selected) != 3 {
		t.Errorf("selection widened past what was asked for: %v", selected)
	}

	var money []LibrarySymbol
	for _, p := range spec.Packages {
		if p.Name == "money" {
			money = p.Symbols
		}
	}
	if len(money) == 0 {
		t.Fatal("money.Money selected nothing")
	}
	methods := 0
	for _, s := range money {
		if s.Name != "Money" && !strings.HasPrefix(s.Name, "Money.") {
			t.Errorf("money.Money pulled in the unrelated symbol %s", s.Name)
		}
		if strings.HasPrefix(s.Name, "Money.") {
			methods++
		}
	}
	// A type whose methods were withheld is the shape that sends a reader
	// straight back to `go doc`.
	if methods == 0 {
		t.Error("money.Money brought back the type but none of its methods")
	}
}

// TestParseSignatureSelectors_UnknownSelectorsFailLoudly: a briefing step
// hands this flag a list somebody wrote weeks ago. When forge/pkg moves
// under it, the wrong outcome is ten units quietly briefed with a hole in
// their API reference.
func TestParseSignatureSelectors_UnknownSelectorsFailLoudly(t *testing.T) {
	t.Parallel()
	dir := repoPkgDir(t)
	pkgs, err := readForgePkgPackages(dir)
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}

	spec := LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
	err = attachSignatures(&spec, "svcerr,dialects")
	if err == nil {
		t.Fatal("a package that does not exist was accepted silently")
	}
	// The message has to name the alternatives, or the caller's next move
	// is the filesystem search this whole verb exists to prevent.
	if !strings.Contains(err.Error(), "svcerr") || !strings.Contains(err.Error(), "orm") {
		t.Errorf("the error does not list what does exist: %v", err)
	}

	spec = LibrariesSpec{Module: forgePkgModule, Dir: dir, Packages: pkgs}
	err = attachSignatures(&spec, "tdd.RPCCases")
	if err == nil {
		t.Fatal("a symbol that does not exist was accepted silently")
	}
	if !strings.Contains(err.Error(), "RPCCase") {
		t.Errorf("the error does not point at the real name: %v", err)
	}
}

// TestWriteLibraries_DefaultStaysTerse: the default output is what every
// existing caller already parses and pastes, so the deep surface must not
// leak into it — but a way in that nobody can discover is one nobody
// takes.
//
// The advertised route used to be `--signatures`, and this test used to
// require the literal `go doc .../svcerr` line beside it. Both moved, for
// the same reason: the flag went undiscovered through a whole measured run
// (35.5 min of grepping instead), and `go doc <pkg>` turned out not to
// reach methods at all. The invariant — terse by default, and it names the
// deeper route — is unchanged; only the route's spelling is.
func TestWriteLibraries_DefaultStaysTerse(t *testing.T) {
	t.Parallel()
	spec := repoLibrariesSpec(t, "")
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, false); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "EXPORTED SIGNATURES") {
		t.Error("the default output grew a signatures section")
	}
	if strings.Contains(out, "func WithReason") {
		t.Error("the default output grew signatures")
	}
	if !strings.Contains(out, "forge project libraries crud") {
		t.Error("the default output does not name the positional form, so nobody will use it")
	}
}

// TestWriteLibraries_SignaturesScopeTheGoDocGuidance: the line that told
// readers to run `go doc .../svcerr` was followed 36 times in one wave.
// With svcerr's API printed below it, that line must stop pointing at
// svcerr — an output that tells a reader to go fetch what it just printed
// is the defect, restated.
func TestWriteLibraries_SignaturesScopeTheGoDocGuidance(t *testing.T) {
	t.Parallel()
	spec := repoLibrariesSpec(t, "svcerr,tdd")
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, false); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "EXPORTED SIGNATURES — svcerr, tdd") {
		t.Errorf("no signatures section:\n%s", out)
	}
	if !strings.Contains(out, "func WithReason(err error, code string) error") {
		t.Error("the section does not carry svcerr's real signatures")
	}
	// The terse index is still there — a reader must still be able to scan
	// all 24 packages in one screen.
	if !strings.Contains(out, "  validate ") {
		t.Error("the one-line inventory was replaced rather than extended")
	}

	guidance, _, _ := strings.Cut(out, "EXPORTED SIGNATURES")
	if strings.Contains(guidance, "go doc "+forgePkgModule+"/svcerr") {
		t.Errorf("the guidance still sends the reader to `go doc svcerr` above svcerr's own API:\n%s", guidance)
	}
	if !strings.Contains(guidance, "go doc "+forgePkgModule+"/") {
		t.Error("the go doc route was dropped entirely — it is still the only way to reach the 22 packages not selected")
	}
	// Whatever it does point at must be a package that really was left
	// out, or the output contradicts itself.
	for _, line := range strings.Split(guidance, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go doc "+forgePkgModule+"/")
		if !ok {
			continue
		}
		named, _, _ := strings.Cut(rest, " ")
		for _, p := range spec.Packages {
			if p.Name == named && len(p.Symbols) > 0 {
				t.Errorf("guidance points at %s, whose API is already printed below", named)
			}
		}
	}
}

// TestBuildLibrariesSpec_SignaturesResolveInThisRepo runs the real
// resolution end to end, so the signatures printed are the ones belonging
// to the pkg version this project compiles against — not to whatever tree
// the forge binary was built beside.
func TestBuildLibrariesSpec_SignaturesResolveInThisRepo(t *testing.T) {
	spec, err := buildLibrariesSpec(context.Background())
	if err != nil {
		t.Fatalf("buildLibrariesSpec: %v", err)
	}
	if err := attachSignatures(&spec, "svcerr"); err != nil {
		t.Fatalf("attachSignatures: %v", err)
	}
	for _, p := range spec.Packages {
		if p.Name != "svcerr" {
			continue
		}
		if len(p.Symbols) == 0 {
			t.Fatalf("svcerr resolved to %s with no symbols", p.Dir)
		}
		return
	}
	t.Fatalf("svcerr missing from the resolved inventory of %s", spec.Dir)
}
